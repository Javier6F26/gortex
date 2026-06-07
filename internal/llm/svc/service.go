// Package svc is the runner layer that ties an llm.Provider to the
// agent tool-loop (package llm/agent) and the search-assist passes
// (assist.go). It lives in its own package to break the import cycle
// that would otherwise exist between `llm` and `llm/agent`.
//
// svc is pure Go: the `-tags llama` build-tag split is contained
// entirely within the provider packages. The daemon links the same
// Service whether or not the tag is set — without it only the `local`
// provider is unavailable; the HTTP providers still work, and a
// disabled service degrades cleanly (Enabled() reports false).
package svc

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/zzet/gortex/internal/llm"
	"github.com/zzet/gortex/internal/llm/agent"
	"github.com/zzet/gortex/internal/llm/provider"
)

// errServiceUnavailable is returned by operational methods when no
// provider could be constructed (disabled config, build without
// `-tags llama` for the local provider, missing API key, ...).
var errServiceUnavailable = errors.New("llm: service unavailable — no provider configured")

// Service is the reusable LLM access point. It wraps a constructed
// llm.Provider plus a Backend (typically an InProcessBackend pointing
// at the daemon's *query.Engine). Three consumption shapes:
//
//   - Generate: one-shot prompt → text. Freeform completion.
//   - RunAgent: the grammar/schema-constrained tool-calling loop that
//     uses the Backend's tools to navigate the graph. Backs the MCP
//     `ask` tool.
//   - ExpandQuery / RerankSymbols / VerifyRelevance: the search-assist
//     passes — short structured completions backing the `search_symbols`
//     `assist` argument (see assist.go).
//
// The active provider is chosen by llm.Config.Provider. The prompt
// tier (profile) is derived from the provider's Name() so the assist
// passes prompt small local models and hosted frontier models
// differently — see llm.ProfileForProvider.
type Service struct {
	cfg         llm.Config
	backend     llm.Backend
	provider    llm.Provider
	providerErr error
	profile     llm.PromptProfile

	expandCache *assistCache
	rerankCache *assistCache
	verifyCache *assistCache

	// routedProviders caches one provider per distinct model id when
	// llm.routing is enabled — see providerForModel. Guarded by
	// routedMu; closed by Close.
	routedMu        sync.Mutex
	routedProviders map[string]llm.Provider
}

// NewService constructs the service and its provider. Provider
// construction is cheap for every backend — the local provider only
// validates its config here and defers the model mmap to the first
// call. A disabled or misconfigured config yields a Service whose
// Enabled() reports false; the construction error is retained and
// surfaced via ProviderErr.
func NewService(cfg llm.Config, backend llm.Backend) *Service {
	cfg = cfg.ApplyDefaults()
	s := &Service{
		cfg:             cfg,
		backend:         backend,
		expandCache:     newAssistCache(256),
		rerankCache:     newAssistCache(256),
		verifyCache:     newAssistCache(256),
		routedProviders: map[string]llm.Provider{},
	}
	if cfg.IsEnabled() && backend != nil {
		p, err := provider.New(cfg)
		if err != nil {
			s.providerErr = err
		} else {
			s.provider = p
			s.profile = llm.ProfileForProvider(p.Name())
		}
	}
	return s
}

// Enabled reports whether the service can do real work — a provider
// was constructed and a backend is wired. Callers gate feature /
// tool registration on this.
func (s *Service) Enabled() bool {
	return s != nil && s.provider != nil && s.backend != nil
}

// Provider returns the underlying LLM provider, or nil when none was
// constructed. Lets callers that need raw completion (e.g. the wiki
// narrative enhancer) reuse the service's already-built provider instead
// of constructing a second one.
func (s *Service) Provider() llm.Provider {
	if s == nil {
		return nil
	}
	return s.provider
}

// ProviderErr returns the error from provider construction, if any.
// Enabled() is false whenever this is non-nil; the daemon entrypoint
// surfaces it as a startup warning so a misconfigured `llm:` block
// (unset API key, model file missing) isn't silently ignored.
func (s *Service) ProviderErr() error {
	if s == nil {
		return nil
	}
	return s.providerErr
}

// ProviderName returns the active provider's name, or "" when no
// provider was constructed.
func (s *Service) ProviderName() string {
	if s == nil || s.provider == nil {
		return ""
	}
	return s.provider.Name()
}

// Close releases the provider's resources (model weights, idle HTTP
// connections), including any providers constructed for model routing.
// Safe to call multiple times and on a disabled service.
func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.routedMu.Lock()
	for _, p := range s.routedProviders {
		_ = p.Close()
	}
	s.routedProviders = map[string]llm.Provider{}
	s.routedMu.Unlock()
	if s.provider == nil {
		return nil
	}
	return s.provider.Close()
}

// providerForModel returns the llm.Provider for the given model id,
// constructing and caching one provider per distinct model. An empty
// model id — or one equal to the active provider's configured model —
// returns the base provider untouched. Routed providers are closed by
// Close. Used by model routing (see RunAgent).
func (s *Service) providerForModel(model string) (llm.Provider, error) {
	model = strings.TrimSpace(model)
	if model == "" || model == strings.TrimSpace(s.cfg.ActiveModel()) {
		return s.provider, nil
	}
	s.routedMu.Lock()
	defer s.routedMu.Unlock()
	if p, ok := s.routedProviders[model]; ok {
		return p, nil
	}
	p, err := provider.New(s.cfg.WithModel(model))
	if err != nil {
		return nil, err
	}
	s.routedProviders[model] = p
	return p, nil
}

// repoCount reports how many repos the backend currently exposes — the
// graph-breadth signal Classify uses for routing. Returns 0 when the
// backend is unavailable or the lookup fails.
func (s *Service) repoCount(ctx context.Context) int {
	if s.backend == nil {
		return 0
	}
	repos, err := s.backend.ListRepos(ctx)
	if err != nil {
		return 0
	}
	return len(repos)
}

// Generate runs one-shot freeform inference: prompt in, generated text
// out. No agent loop, no tools. maxTokens caps generation length; 0
// falls back to a sensible default.
func (s *Service) Generate(ctx context.Context, prompt string, maxTokens int) (string, error) {
	if s.provider == nil {
		return "", errServiceUnavailable
	}
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	resp, err := s.provider.Complete(ctx, llm.CompletionRequest{
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: prompt}},
		MaxTokens: maxTokens,
		Shape:     llm.ShapeFreeform,
	})
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}

// RunAgent runs the structured tool-calling agent loop. The agent
// issues tool calls against the configured Backend and synthesizes a
// final answer via the final_answer tool.
//
// The returned AgentAnswer always has at least Answer/Error populated
// — non-nil even on error paths.
func (s *Service) RunAgent(ctx context.Context, opts llm.RunAgentOptions) (*llm.AgentAnswer, error) {
	answer := &llm.AgentAnswer{Scope: opts.Scope, ChainMode: opts.Chain}
	if s.provider == nil {
		answer.Error = errServiceUnavailable.Error()
		return answer, errServiceUnavailable
	}
	if strings.TrimSpace(opts.Question) == "" {
		err := errors.New("llm: question is empty")
		answer.Error = err.Error()
		return answer, err
	}

	systemExtras := opts.SystemExtras
	if systemExtras == "" {
		if opts.Chain {
			systemExtras = promptChain
		} else {
			systemExtras = promptSimple
		}
	}

	var tools []agent.Tool
	if opts.Chain {
		tools = agent.GortexChainTools(s.backend, opts.Scope)
	} else {
		tools = agent.GortexTools(s.backend, opts.Scope)
	}

	// Model routing: when enabled, classify the run by graph-derived
	// task complexity and dispatch it to the cheaper or the more
	// capable model. A routing failure (a tier model that won't
	// construct) degrades cleanly to the base provider.
	activeProvider := s.provider
	answer.Model = s.cfg.ActiveModel()
	if s.cfg.Routing.Enabled {
		complexity := llm.Classify(llm.ComplexitySignals{
			Question:  opts.Question,
			Chain:     opts.Chain,
			Scoped:    opts.Scope.Repo != "" || opts.Scope.Project != "" || opts.Scope.Ref != "",
			RepoCount: s.repoCount(ctx),
		})
		answer.Complexity = complexity.String()
		routedModel := s.cfg.Routing.SimpleModel
		if complexity == llm.ComplexityComplex {
			routedModel = s.cfg.Routing.ComplexModel
		}
		if p, perr := s.providerForModel(routedModel); perr == nil && p != nil {
			activeProvider = p
			if m := strings.TrimSpace(routedModel); m != "" {
				answer.Model = m
			}
		}
	}

	ag, err := agent.New(activeProvider, tools)
	if err != nil {
		answer.Error = err.Error()
		return answer, err
	}

	t0 := time.Now()
	answerText, transcript, runErr := ag.Run(ctx, systemExtras, opts.Question, s.cfg.MaxSteps)
	answer.ElapsedMs = time.Since(t0).Milliseconds()
	answer.Answer = answerText

	steps := 0
	for _, st := range transcript {
		if st.Kind == "call" || st.Kind == "final" {
			steps++
		}
	}
	answer.Steps = steps

	if opts.IncludeTranscript {
		answer.Transcript = make([]llm.TranscriptStep, 0, len(transcript))
		for _, st := range transcript {
			answer.Transcript = append(answer.Transcript, llm.TranscriptStep{
				Kind: st.Kind, Raw: st.Raw, Tool: st.Tool,
			})
		}
	}
	if runErr != nil {
		answer.Error = runErr.Error()
	}
	return answer, runErr
}

// promptSimple — tight system-prompt extras for single-hop /
// cross-repo lookups.
const promptSimple = `RULES (follow these exactly):
- If the user gives you only a bare name (not a path-qualified id like "pkg/x.Foo"), you MUST first call search_symbols to resolve it to an id before calling get_callers.
- For search_symbols, pass ONLY the bare symbol name as "query" — no prepositions, no package qualifiers, no extra words.
- search_symbols returns ranked matches; the FIRST few are best. Pick at most the top 1-3 that look like functions or methods.
- Make at least one real tool call before final_answer.
- Never call the same tool with the same args twice in a row.
- When you have enough information, call final_answer summarising what you found.`

// promptChain — chain-mode extras with the explicit "no get_callers"
// direction warning.
const promptChain = `RULES (follow these exactly):
- You are tracing a cross-system call chain. Output one tool call per turn.
- DIRECTION MATTERS. Only these tools are correct in chain mode:
    * contracts        — find producer↔consumer pairs across repos
    * get_dependencies — FORWARD direction: what does this symbol call/import?
    * final_answer     — emit the chain
  Do NOT use get_callers. get_callers walks BACKWARDS (who calls X), which is
  the WRONG direction for chain tracing and will lead you astray.
- For search_symbols and contracts, pass clean values (no extra words).
- Typical flow for "trace request X across systems":
  1) contracts({"role":"consumer","path":"<path>"}) — find the caller side.
  2) contracts({"role":"provider","path":"<path>"}) — find the handler.
  3) get_dependencies({"id":"<provider symbol_id>"}) — see what the handler calls.
  4) For deeper hops, call get_dependencies AGAIN on the most interesting result's id.
  5) Look for deps whose repo prefix differs from the handler's repo —
     those are the cross-repo downstream calls.
  6) Call final_answer with the chain as numbered steps.
- Never call the same tool with the same args twice in a row.
- final_answer.text should list each system hop with its symbol id and repo.`
