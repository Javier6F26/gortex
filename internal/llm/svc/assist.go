//go:build llama

package svc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/zzet/gortex/internal/llm"
	"github.com/zzet/gortex/internal/llm/agent"
)

// assistCtxSize is the KV-cache window for the short-call assist
// context. Sized for the heaviest user — verify with body + callers
// at ~3.5K tokens for 10 candidates. Expansion and rerank use a
// fraction of this; the extra KV cache is cheap (a few hundred MB).
const assistCtxSize = 4096

// Token caps per call. Expansion emits at most a small JSON list;
// rerank emits at most one ID per candidate. Verify emits one ID per
// surviving candidate, so its cap is comparable to rerank.
const (
	expandMaxTokens = 192
	rerankMaxTokens = 512
	verifyMaxTokens = 512
)

// Grammar for {"terms":[<string>, ...]}. Strings are arbitrary JSON
// strings — callers filter the output to whatever's actually useful.
const expandGrammar = `root ::= ws "{" ws "\"terms\"" ws ":" ws "[" ws ( str ( ws "," ws str )* )? ws "]" ws "}" ws
str ::= "\"" ( [^"\\] | "\\" ( ["\\/bfnrt] | "u" [0-9a-fA-F] [0-9a-fA-F] [0-9a-fA-F] [0-9a-fA-F] ) )* "\""
ws ::= [ \t\n]*
`

// Grammar for {"order":[<string>, ...]}. Same shape as expand,
// different top-level key — kept as two constants so each call site
// skips a Sprintf on the hot path.
const rerankGrammar = `root ::= ws "{" ws "\"order\"" ws ":" ws "[" ws ( str ( ws "," ws str )* )? ws "]" ws "}" ws
str ::= "\"" ( [^"\\] | "\\" ( ["\\/bfnrt] | "u" [0-9a-fA-F] [0-9a-fA-F] [0-9a-fA-F] [0-9a-fA-F] ) )* "\""
ws ::= [ \t\n]*
`

// Grammar for {"keep":[<string>, ...]}. The body-grounded verifier
// MUST be allowed to emit an empty array — that's the load-bearing
// "honest negative" signal — so the array body is fully optional.
const verifyGrammar = `root ::= ws "{" ws "\"keep\"" ws ":" ws "[" ws ( str ( ws "," ws str )* )? ws "]" ws "}" ws
str ::= "\"" ( [^"\\] | "\\" ( ["\\/bfnrt] | "u" [0-9a-fA-F] [0-9a-fA-F] [0-9a-fA-F] [0-9a-fA-F] ) )* "\""
ws ::= [ \t\n]*
`

const expandSystem = `You expand a code-search query into a small set of CONCRETE identifier-style terms a programmer would actually grep for. ` +
	`Output strict JSON: {"terms":["<term1>","<term2>",...]}. ` +
	`Include 2 to 5 terms. Each term MUST be a single word with no spaces and no punctuation other than underscores. ` +
	`
RULES:
1. Prefer DOMAIN-SPECIFIC terms over generic English. ` +
	`GOOD examples: bcrypt, argon2, scrypt, sha256, hmac, jwt, oauth, pbkdf2, kdf, salt. ` +
	`BAD examples (NEVER emit): function, library, algorithm, code, system, data, service, value, info, content, thing, stuff, name, general, common, logic, process, handler, flow, action, helper, util, utility. ` +
	`
2. Prefer terms that are likely SYMBOL names in a codebase (camelCase / snake_case / PascalCase fragments), library or protocol names, well-known acronyms. ` +
	`
3. Do NOT echo the original query words. ` +
	`
4. If the query has no obvious domain-specific neighbours, emit FEWER terms (or an empty array) — quality over quantity.`

const rerankSystem = `You rerank code-search results by relevance to a natural-language task. ` +
	`Given a query and a list of candidate symbols (id | name | optional signature), output strict JSON: {"order":["id1","id2",...]} ` +
	`with the most relevant candidates first. ` +
	`Use ONLY the provided ids verbatim. Do not invent ids. You may drop ids that are clearly unrelated.`

const verifySystem = `You filter code-search candidates by reading their BODY, SIGNATURE, and CALLERS, and keeping every one whose code is genuinely about the user's query. ` +
	`Each candidate is presented as:

<id> | <name> | <signature>
body:
<code body, truncated>
callers:
- <caller_name> | <caller_signature>
- ...
---

Output strict JSON: {"keep":["id1","id2",...]} listing EVERY id whose code is meaningfully related to the query, in your preferred order (most relevant first).

RULES (follow exactly):
1. Evaluate EACH candidate INDEPENDENTLY. Multiple candidates can be valid matches — keep them all.
2. A name that contains a query word is not enough by itself — read what the code DOES.
3. Cross-reference the CALLERS and the SIGNATURE's parameter types against the query DOMAIN. If a function hashes data but is only called from a "publishDiagnostics" or "renderLog" path with a non-password parameter type, it is NOT about hashing passwords — DROP it.
4. Be GENEROUS, not restrictive: if a candidate's body AND callers AND signature are all plausibly about the query, KEEP it. The user wants signal, not a single "best" pick.
5. Drop a candidate when its body, signature, or callers reveal the operation is on the wrong KIND of data for the query.
6. Returning {"keep":[]} is valid ONLY when NO candidate is genuinely about the query.
7. Use ONLY the provided ids verbatim. Never invent or modify an id.`

// ensureAssist lazily allocates the short-call context the first time
// an assist method is called. Safe to invoke before locking
// assistMu — the underlying sync.Once handles concurrent first calls.
// Subsequent callers MUST still take assistMu before touching
// assistCtx, since the context itself is single-stream.
func (s *Service) ensureAssist() error {
	if err := s.ensureLoaded(); err != nil {
		return err
	}
	s.assistOnce.Do(func() {
		c, err := s.model.NewContext(assistCtxSize, 0)
		if err != nil {
			s.assistErr = fmt.Errorf("llm: assist context: %w", err)
			return
		}
		s.assistCtx = c
	})
	return s.assistErr
}

// ExpandQuery turns a natural-language search query into a small set
// of related identifier-style terms via one grammar-constrained
// inference pass. Result is cached by query string. Empty / blank
// input returns an empty result without touching the model.
//
// The caller is expected to OR the returned terms with the original
// query and rerank by combined BM25 score.
func (s *Service) ExpandQuery(ctx context.Context, query string) (*llm.ExpandResult, error) {
	_ = ctx
	query = strings.TrimSpace(query)
	if query == "" {
		return &llm.ExpandResult{Original: query}, nil
	}

	if cached, ok := s.expandCache.Get(query); ok {
		return &llm.ExpandResult{Original: query, Terms: cached, Cached: true}, nil
	}
	if err := s.ensureAssist(); err != nil {
		return nil, err
	}

	tmpl, err := agent.TemplateByName(s.cfg.Template)
	if err != nil {
		return nil, err
	}
	prompt := buildAssistPrompt(tmpl, expandSystem, "Query: "+query)

	raw, err := s.runAssist(prompt, expandGrammar, expandMaxTokens)
	if err != nil {
		return nil, err
	}

	terms := parseStringList(raw, "terms")
	terms = dedupeFilter(terms, query)
	// Even an empty result is worth caching — re-issuing the prompt
	// won't change a model that consistently emits nothing useful.
	s.expandCache.Set(query, terms)
	return &llm.ExpandResult{Original: query, Terms: terms}, nil
}

// RerankSymbols asks the model to reorder a candidate set by
// relevance to the query. IDs the model drops are appended at the
// tail in original input order so the caller never loses a candidate.
// Empty input returns an empty order without touching the model.
//
// Cache key includes the candidate ID set so two callers passing the
// same query against different candidate pools each get their own
// cache entry; ordering of input candidates does not affect the key.
func (s *Service) RerankSymbols(ctx context.Context, query string, cands []llm.RerankCandidate) (*llm.RerankResult, error) {
	_ = ctx
	query = strings.TrimSpace(query)
	if query == "" || len(cands) == 0 {
		return &llm.RerankResult{Order: candIDs(cands)}, nil
	}

	key := rerankCacheKey(query, cands)
	if cached, ok := s.rerankCache.Get(key); ok {
		return &llm.RerankResult{Order: cached, Cached: true}, nil
	}
	if err := s.ensureAssist(); err != nil {
		return nil, err
	}

	tmpl, err := agent.TemplateByName(s.cfg.Template)
	if err != nil {
		return nil, err
	}
	user := buildRerankUser(query, cands)
	prompt := buildAssistPrompt(tmpl, rerankSystem, user)

	raw, err := s.runAssist(prompt, rerankGrammar, rerankMaxTokens)
	if err != nil {
		// Surface the error but keep input order intact so the caller
		// can still return *something* — search-assist must never
		// degrade below baseline BM25 quality.
		return &llm.RerankResult{Order: candIDs(cands)}, err
	}

	rawOrder := parseStringList(raw, "order")
	order := filterToInputAppend(rawOrder, cands)
	s.rerankCache.Set(key, order)
	return &llm.RerankResult{Order: order}, nil
}

// VerifyRelevance reads each candidate's code body and returns only
// the IDs the model judges genuinely related to the query — an empty
// list means "no candidate's code actually does what was asked",
// which is a load-bearing honest-negative signal the caller should
// preserve rather than fall back to BM25 noise.
//
// Cache key includes (query, sorted IDs, body hash) so a re-indexed
// codebase doesn't return stale verifications. Empty input short-
// circuits without touching the model.
//
// On any inference or parse failure, returns the input order
// unchanged with the error — the caller should treat that as "could
// not verify" rather than "nothing matched".
func (s *Service) VerifyRelevance(ctx context.Context, query string, cands []llm.VerifyCandidate) (*llm.VerifyResult, error) {
	_ = ctx
	query = strings.TrimSpace(query)
	if query == "" || len(cands) == 0 {
		return &llm.VerifyResult{Keep: verifyIDs(cands)}, nil
	}

	key := verifyCacheKey(query, cands)
	if cached, ok := s.verifyCache.Get(key); ok {
		return &llm.VerifyResult{Keep: cached, Cached: true}, nil
	}
	if err := s.ensureAssist(); err != nil {
		return nil, err
	}

	tmpl, err := agent.TemplateByName(s.cfg.Template)
	if err != nil {
		return nil, err
	}
	user := buildVerifyUser(query, cands)
	prompt := buildAssistPrompt(tmpl, verifySystem, user)

	raw, err := s.runAssist(prompt, verifyGrammar, verifyMaxTokens)
	if err != nil {
		// On failure, surface the error and keep all input candidates
		// — better to over-include than to silently drop them.
		return &llm.VerifyResult{Keep: verifyIDs(cands)}, err
	}

	rawKeep := parseStringList(raw, "keep")
	keep := filterKeepToInput(rawKeep, cands)
	s.verifyCache.Set(key, keep)
	return &llm.VerifyResult{Keep: keep}, nil
}

// runAssist is the shared inference primitive for the two assist
// methods. Holds assistMu, resets KV cache, installs the grammar,
// generates with the jsonComplete early-stop predicate, and returns
// the raw model output trimmed of surrounding whitespace.
func (s *Service) runAssist(prompt, grammar string, maxTokens int) (string, error) {
	s.assistMu.Lock()
	defer s.assistMu.Unlock()

	if s.assistCtx == nil {
		return "", errors.New("llm: assist context not initialised")
	}

	s.assistCtx.Reset()
	if err := s.assistCtx.SetGrammar(grammar); err != nil {
		return "", fmt.Errorf("llm: install assist grammar: %w", err)
	}

	var buf strings.Builder
	_, err := s.assistCtx.Generate(prompt, maxTokens, func(piece string) bool {
		buf.WriteString(piece)
		return !assistJSONComplete(buf.String())
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

// buildAssistPrompt is the single-turn equivalent of agent.initialPrompt:
// no tool list, no AssistEnd round-trip — just System + User + AssistPrime.
func buildAssistPrompt(tmpl agent.ChatTemplate, system, user string) string {
	return tmpl.BOS + tmpl.System(system) + tmpl.User(user) + tmpl.AssistPrime
}

// buildVerifyUser formats the candidate list for the body-grounded
// verify prompt. Each candidate ships with its body and a compact
// callers block — the callers carry independent contextual signal
// that lets the model distinguish "same operation, different data"
// cases the body alone can't disambiguate. Bodies and signatures
// must be pre-truncated by the caller — this is a formatter, not
// the place to enforce length limits.
func buildVerifyUser(query string, cands []llm.VerifyCandidate) string {
	var b strings.Builder
	b.WriteString("Query: ")
	b.WriteString(query)
	b.WriteString("\n\nCandidates:\n")
	for _, c := range cands {
		b.WriteString(c.ID)
		b.WriteString(" | ")
		b.WriteString(c.Name)
		if sig := strings.TrimSpace(c.Signature); sig != "" {
			b.WriteString(" | ")
			if len(sig) > 160 {
				sig = sig[:160] + "…"
			}
			b.WriteString(sig)
		}
		b.WriteString("\nbody:\n")
		if body := strings.TrimSpace(c.Body); body != "" {
			b.WriteString(body)
			if !strings.HasSuffix(body, "\n") {
				b.WriteString("\n")
			}
		} else {
			b.WriteString("(no body — signature-only)\n")
		}
		if len(c.Callers) > 0 {
			b.WriteString("callers:\n")
			for _, cl := range c.Callers {
				b.WriteString("- ")
				b.WriteString(cl.Name)
				if sig := strings.TrimSpace(cl.Signature); sig != "" {
					b.WriteString(" | ")
					if len(sig) > 120 {
						sig = sig[:120] + "…"
					}
					b.WriteString(sig)
				}
				b.WriteString("\n")
			}
		} else {
			b.WriteString("callers: (none indexed)\n")
		}
		b.WriteString("---\n")
	}
	return b.String()
}

// buildRerankUser formats the candidate list for the rerank prompt.
// One line per candidate: "id | name | signature?". Truncates very
// long signatures so a single noisy entry can't blow the context.
func buildRerankUser(query string, cands []llm.RerankCandidate) string {
	var b strings.Builder
	b.WriteString("Query: ")
	b.WriteString(query)
	b.WriteString("\nCandidates:\n")
	for _, c := range cands {
		b.WriteString("- ")
		b.WriteString(c.ID)
		b.WriteString(" | ")
		b.WriteString(c.Name)
		if sig := strings.TrimSpace(c.Signature); sig != "" {
			b.WriteString(" | ")
			if len(sig) > 120 {
				sig = sig[:120] + "…"
			}
			b.WriteString(sig)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// assistJSONComplete is the same shape as agent.jsonComplete: stop
// generation as soon as the top-level JSON object closes and parses.
// Replicated rather than exported from package agent to keep that
// package's surface minimal.
func assistJSONComplete(s string) bool {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		return false
	}
	var v any
	return json.Unmarshal([]byte(s), &v) == nil
}

// parseStringList extracts a top-level JSON string array under the
// given key. Returns nil on any parse failure — the caller decides
// the fallback behaviour.
func parseStringList(raw, key string) []string {
	if raw == "" {
		return nil
	}
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	v, ok := m[key]
	if !ok {
		return nil
	}
	var out []string
	if err := json.Unmarshal(v, &out); err != nil {
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// expansionStoplist is the conservative list of generic English nouns
// that the BM25 layer matches against thousands of unrelated symbols
// (e.g. `function`, `data`, `library`). These rarely carry useful
// search signal on their own and almost always inflate the candidate
// pool with noise. Members were chosen by inspecting real expansion
// outputs from Qwen2.5-Coder 3B against the gortex corpus — words
// that produced no relevant additional hits but many irrelevant ones.
//
// Borderline / domain-bearing words like `encryption`, `algorithm`,
// `security`, `key` are deliberately NOT here: they can be load-bearing
// in some codebases (a crypto library is a different story than a code
// intelligence tool). Keep this list short — over-filtering throws
// away the only signal expansion has to offer.
var expansionStoplist = map[string]bool{
	"function": true, "functions": true, "method": true, "methods": true,
	"library": true, "libraries": true,
	"module": true, "modules": true, "package": true, "packages": true,
	"system": true, "systems": true,
	"service": true, "services": true,
	"code": true, "codes": true, "source": true,
	"data": true, "datum": true,
	"value": true, "values": true,
	"object": true, "objects": true, "item": true, "items": true,
	"thing":   true, "things": true,
	"info":    true, "information": true,
	"content": true, "contents": true,
	"stuff":   true,
	"general": true, "common": true, "basic": true, "simple": true, "main": true,
	"text":    true,
	// Generic verbs/nouns that slip through with NL queries — observed
	// in the wild: "where is the rerank logic for search results" pulled
	// in "logic" as an expansion term, which broadens BM25 enormously
	// against any *_logic or logical_* identifier.
	"logic": true, "logical": true,
	"process": true, "processing": true,
	"handle":  true, "handler": true, "handling": true,
	"flow":    true, "flows": true,
	"action":  true, "actions": true,
	"helper":  true, "helpers": true,
	"util":    true, "utils": true, "utility": true, "utilities": true,
}

// minExpansionTermLen rejects terms shorter than this. Sub-3 char
// fragments (`do`, `is`, `id`) generate huge BM25 hit lists and
// almost never carry useful signal. The threshold is conservative —
// short identifiers like `js`, `db`, `ui` get through.
const minExpansionTermLen = 3

// dedupeFilter trims, lowercases for comparison, and drops terms that
// are empty, duplicates, the original query, in expansionStoplist, or
// shorter than minExpansionTermLen. Preserves order of the surviving
// terms. The cap at maxExpansionTerms keeps the merged candidate pool
// bounded even when the model ignores the "2 to 5" prompt instruction.
func dedupeFilter(terms []string, query string) []string {
	queryLower := strings.ToLower(strings.TrimSpace(query))
	seen := map[string]bool{queryLower: true}
	out := make([]string, 0, len(terms))
	for _, t := range terms {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		k := strings.ToLower(t)
		if seen[k] || expansionStoplist[k] {
			continue
		}
		if len(t) < minExpansionTermLen {
			continue
		}
		seen[k] = true
		out = append(out, t)
		if len(out) >= maxExpansionTerms {
			break
		}
	}
	return out
}

// maxExpansionTerms caps the per-call expansion regardless of model
// output. Each extra term adds a BM25 sweep + candidate-pool growth,
// so trimming aggressively saves both latency and rerank prompt size.
const maxExpansionTerms = 5

// candIDs extracts just the ID slice from a candidate list,
// preserving order. Returned for fallback paths so the caller still
// gets a valid (if unhelpful) ordering.
func candIDs(cands []llm.RerankCandidate) []string {
	if len(cands) == 0 {
		return nil
	}
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.ID
	}
	return out
}

// verifyIDs is the VerifyCandidate equivalent of candIDs — used on
// fallback paths where we want to preserve every input ID rather
// than drop them silently.
func verifyIDs(cands []llm.VerifyCandidate) []string {
	if len(cands) == 0 {
		return nil
	}
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.ID
	}
	return out
}

// filterKeepToInput is the VerifyResult equivalent of
// filterToInputAppend but with one critical difference: dropped IDs
// are NOT appended at the tail. An empty result IS the load-bearing
// honest-negative signal, so callers must see exactly what the
// model decided to keep.
//
// Hallucinated and duplicate IDs are still filtered defensively.
func filterKeepToInput(modelKeep []string, cands []llm.VerifyCandidate) []string {
	valid := make(map[string]bool, len(cands))
	for _, c := range cands {
		valid[c.ID] = true
	}
	used := make(map[string]bool, len(cands))
	out := make([]string, 0, len(modelKeep))
	for _, id := range modelKeep {
		if !valid[id] || used[id] {
			continue
		}
		used[id] = true
		out = append(out, id)
	}
	return out
}

// filterToInputAppend builds the final rerank order: every model ID
// that matches an input candidate, in model-supplied order, then any
// remaining input IDs in their original order. This makes the result
// a stable permutation of the input set even when the model drops or
// hallucinates entries.
func filterToInputAppend(modelOrder []string, cands []llm.RerankCandidate) []string {
	valid := make(map[string]bool, len(cands))
	for _, c := range cands {
		valid[c.ID] = true
	}
	used := make(map[string]bool, len(cands))
	out := make([]string, 0, len(cands))
	for _, id := range modelOrder {
		if !valid[id] || used[id] {
			continue
		}
		used[id] = true
		out = append(out, id)
	}
	for _, c := range cands {
		if !used[c.ID] {
			out = append(out, c.ID)
		}
	}
	return out
}
