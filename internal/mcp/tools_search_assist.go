package mcp

import (
	"context"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/llm"
	"github.com/zzet/gortex/internal/query"
)

// assistMode controls whether the LLM-driven query expansion and
// rerank passes run during handleSearchSymbols. Default is `auto` —
// the NL heuristic decides per-query. `on` and `off` are explicit
// overrides for callers that know their query character. `deep` is
// `on` plus a body-grounded verification pass that reads candidate
// code bodies and HONESTLY drops the ones whose code is not
// actually about the query (paid for in extra latency).
type assistMode int

const (
	assistAuto assistMode = iota
	assistOn
	assistOff
	assistDeep
)

// parseAssistMode reads the `assist` arg. Unrecognised values fall
// back to `auto` rather than erroring so callers can't accidentally
// break search by typoing the flag.
func parseAssistMode(req mcpgo.CallToolRequest) assistMode {
	switch strings.ToLower(strings.TrimSpace(req.GetString("assist", ""))) {
	case "on", "yes", "true", "force":
		return assistOn
	case "off", "no", "false", "skip":
		return assistOff
	case "deep", "verify", "body":
		return assistDeep
	default:
		return assistAuto
	}
}

// looksNaturalLanguage is the cheap pre-LLM gate. Returns true when
// the query is shaped like a natural-language description rather than
// an identifier lookup. Heuristics:
//   - Fewer than 3 whitespace tokens → identifier; skip.
//   - Any token containing dot / slash / scope-resolution → qualified
//     identifier; skip.
//   - Any token that's PascalCase or camelCase → identifier; skip.
//   - At least one common English stop word among 3+ tokens → engage.
//   - 4+ plain-word tokens with no identifier shape → engage.
//
// Empty / blank input never engages.
func looksNaturalLanguage(q string) bool {
	q = strings.TrimSpace(q)
	if q == "" {
		return false
	}
	tokens := strings.Fields(q)
	if len(tokens) < 3 {
		return false
	}
	for _, t := range tokens {
		if strings.ContainsAny(t, "./:_") {
			return false
		}
		if hasMixedCase(t) {
			return false
		}
	}
	if hasStopWord(tokens) {
		return true
	}
	return len(tokens) >= 4
}

// hasMixedCase reports whether a token contains both upper and lower
// ASCII letters — i.e. PascalCase or camelCase. Pure lowercase /
// pure uppercase plain words don't qualify.
func hasMixedCase(t string) bool {
	var hasUpper, hasLower bool
	for _, r := range t {
		switch {
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= 'a' && r <= 'z':
			hasLower = true
		}
		if hasUpper && hasLower {
			return true
		}
	}
	return false
}

// assistStopWords is a tight list of English function words that
// rarely appear in identifier-style queries. Matching any of them in
// a 3+ token query strongly signals natural language. Kept short
// deliberately — false positives here cost LLM latency on every call.
var assistStopWords = map[string]struct{}{
	"where": {}, "how": {}, "what": {}, "why": {}, "which": {}, "when": {},
	"the": {}, "is": {}, "a": {}, "an": {},
	"in": {}, "of": {}, "to": {}, "for": {}, "with": {}, "by": {}, "from": {},
	"do": {}, "does": {}, "are": {}, "we": {}, "us": {}, "our": {},
	"and": {}, "or": {}, "not": {},
}

func hasStopWord(tokens []string) bool {
	for _, t := range tokens {
		if _, ok := assistStopWords[strings.ToLower(t)]; ok {
			return true
		}
	}
	return false
}

// shouldEngageAssist combines the caller's explicit mode with the
// auto-gate heuristic. `on` and `deep` always engage, `off` never
// engages, and `auto` defers to looksNaturalLanguage. The service-
// side enabled check is layered on top — callers wrap this with
// `s.llmService != nil && s.llmService.Enabled()` so that a stub
// build short-circuits regardless of mode.
func shouldEngageAssist(mode assistMode, query string) bool {
	switch mode {
	case assistOff:
		return false
	case assistOn, assistDeep:
		return true
	default:
		return looksNaturalLanguage(query)
	}
}

// expandSearchTerms calls the LLM expansion path and returns the
// extra terms. Returns nil (no expansion) on any failure so the
// search path stays at parity with today's behaviour when the model
// hiccups or isn't loaded yet.
func expandSearchTerms(ctx context.Context, s *Server, query string) []string {
	if s.llmService == nil || !s.llmService.Enabled() {
		return nil
	}
	res, err := s.llmService.ExpandQuery(ctx, query)
	if err != nil || res == nil {
		return nil
	}
	return res.Terms
}

// fetchAndMergeBM25 runs BM25 once per term (original + expansions),
// then folds the results into a single deduplicated slice. The
// original query's hits win position; expansion hits append in their
// own BM25 order with duplicates skipped.
//
// fetchLimit is the per-term over-fetch budget. Bounded by the caller
// so a wide expansion can't blow up the candidate pool.
//
// primaryCount is the size of the original-query BM25 result before
// merging; useful for diagnostic / debug surfaces that want to show
// how many candidates expansion contributed.
func fetchAndMergeBM25(s *Server, original string, expanded []string, fetchLimit int, scope query.QueryOptions) (merged []*graph.Node, primaryCount int) {
	primary := s.engine.SearchSymbolsScoped(original, fetchLimit, scope)
	primaryCount = len(primary)
	if len(expanded) == 0 {
		return primary, primaryCount
	}
	seen := make(map[string]bool, len(primary))
	merged = make([]*graph.Node, 0, len(primary))
	for _, n := range primary {
		if seen[n.ID] {
			continue
		}
		seen[n.ID] = true
		merged = append(merged, n)
	}
	for _, term := range expanded {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		extra := s.engine.SearchSymbolsScoped(term, fetchLimit, scope)
		for _, n := range extra {
			if seen[n.ID] {
				continue
			}
			seen[n.ID] = true
			merged = append(merged, n)
		}
	}
	return merged, primaryCount
}

// rerankCap bounds how many candidates the rerank pass sees. The
// model has limited working memory; past ~25 items its judgement
// degrades and the prompt blows the assist context. Trailing
// candidates beyond rerankCap stay in BM25 order and are appended
// after the reranked head.
const rerankCap = 20

// prioritizeCallables stably re-orders nodes so functions and methods
// come first, preserving BM25 order within each bucket. Everything
// non-callable (fields, params, variables, constants, types, files,
// imports, …) sinks to the tail in its original order. The intent
// is to make sure the rerank head — which is what the LLM sees and
// reorders — is populated with the symbols that actually *do* things,
// not their structural siblings that just happen to share tokens.
func prioritizeCallables(nodes []*graph.Node) []*graph.Node {
	callable := make([]*graph.Node, 0, len(nodes))
	others := make([]*graph.Node, 0, len(nodes))
	for _, n := range nodes {
		if n.Kind == graph.KindFunction || n.Kind == graph.KindMethod {
			callable = append(callable, n)
		} else {
			others = append(others, n)
		}
	}
	return append(callable, others...)
}

// verifyCap bounds how many candidates the body-grounded verifier
// sees. Each candidate ships with its function body (truncated), so
// the input is much heavier than the name+sig rerank — keep it
// smaller to stay inside the assist context.
const verifyCap = 10

// verifyBodyMaxLines and verifyBodyMaxChars cap the per-candidate
// body fed to the model. We want enough to see what the code DOES
// (a function header + a few lines of logic) without including
// every helper call. Empirically 8 non-blank lines is plenty for
// the verify decision.
const (
	verifyBodyMaxLines = 8
	verifyBodyMaxChars = 600
)

// verifyCallersPerCand caps the number of callers sent per candidate.
// More callers = more disambiguation signal, but also more tokens.
// Three is empirically enough to anchor the data-domain of most
// functions without blowing the assist context for a 10-candidate batch.
const verifyCallersPerCand = 3

// topCallersForVerify returns up to verifyCallersPerCand callers of n,
// each with name + truncated signature. The query depth is 1 (direct
// callers only) and the brief detail level keeps memory pressure low.
// Returns nil for non-callable kinds or when GetCallers yields nothing.
func topCallersForVerify(s *Server, n *graph.Node) []llm.CallerInfo {
	if n.Kind != graph.KindFunction && n.Kind != graph.KindMethod {
		return nil
	}
	sg := s.engine.GetCallers(n.ID, query.QueryOptions{
		Depth:  1,
		Limit:  verifyCallersPerCand + 4, // over-fetch a little: self + non-callers get filtered
		Detail: "brief",
	})
	if sg == nil || len(sg.Nodes) == 0 {
		return nil
	}
	out := make([]llm.CallerInfo, 0, verifyCallersPerCand)
	for _, cn := range sg.Nodes {
		if cn == nil || cn.ID == n.ID {
			continue
		}
		if cn.Kind != graph.KindFunction && cn.Kind != graph.KindMethod {
			continue
		}
		sig, _ := cn.Meta["signature"].(string)
		out = append(out, llm.CallerInfo{
			Name:      cn.Name,
			Signature: sig,
		})
		if len(out) >= verifyCallersPerCand {
			break
		}
	}
	return out
}

// extractBodyForVerify reads a node's source body, returns the first
// verifyBodyMaxLines non-blank lines truncated to verifyBodyMaxChars.
// Returns "" when no source can be read or when the node isn't a
// function/method — non-function symbols pass through to the verifier
// with signature-only context, which the prompt handles explicitly.
func extractBodyForVerify(s *Server, n *graph.Node) string {
	if n.Kind != graph.KindFunction && n.Kind != graph.KindMethod {
		return ""
	}
	if n.StartLine <= 0 || n.EndLine <= 0 {
		return ""
	}
	abs, err := s.resolveNodePath(n)
	if err != nil {
		return ""
	}
	source, _, _, err := readLines(abs, n.StartLine, n.EndLine, 0)
	if err != nil {
		return ""
	}
	return truncateBody(source, verifyBodyMaxLines, verifyBodyMaxChars)
}

// truncateBody keeps the first maxLines non-blank lines, then
// caps the result at maxChars. Blank lines between code count
// against neither budget — they're skipped. Returns the truncated
// text with a trailing "…" marker when either cap fires.
func truncateBody(src string, maxLines, maxChars int) string {
	if src == "" {
		return ""
	}
	lines := strings.Split(src, "\n")
	var b strings.Builder
	kept := 0
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(ln)
		b.WriteString("\n")
		kept++
		if kept >= maxLines {
			b.WriteString("…\n")
			break
		}
	}
	out := b.String()
	if len(out) > maxChars {
		out = out[:maxChars] + "…\n"
	}
	return out
}

// verifyDebug captures what the verify pass saw and decided, so the
// debug surface can return it for diagnostic inspection. Lightweight
// — only ID lists, no bodies.
type verifyDebug struct {
	Considered []string // IDs sent to the verifier (top-verifyCap of head)
	Kept       []string // IDs the model chose to keep, in keep order
}

// verifyWithLLM runs the body-grounded verification pass on the head
// of `nodes`. Returns the model's kept-and-ordered subset followed
// by anything past verifyCap (unverified tail). On failure or empty
// service the input is returned unchanged.
//
// An empty `keep` is HONORED: when the model says "nothing here
// matches", we return only the unverified tail. The caller is meant
// to treat that as a legitimate negative result rather than fall back
// to the noisy pre-verify candidates.
func verifyWithLLM(ctx context.Context, s *Server, query string, nodes []*graph.Node) (result []*graph.Node, dbg verifyDebug, ok bool) {
	if s.llmService == nil || !s.llmService.Enabled() || len(nodes) == 0 {
		return nodes, dbg, false
	}
	head := nodes
	tail := []*graph.Node(nil)
	if len(nodes) > verifyCap {
		head = nodes[:verifyCap]
		tail = nodes[verifyCap:]
	}

	cands := make([]llm.VerifyCandidate, len(head))
	idx := make(map[string]*graph.Node, len(head))
	dbg.Considered = make([]string, len(head))
	for i, n := range head {
		sig, _ := n.Meta["signature"].(string)
		cands[i] = llm.VerifyCandidate{
			ID:        n.ID,
			Name:      n.Name,
			Signature: sig,
			Body:      extractBodyForVerify(s, n),
			Callers:   topCallersForVerify(s, n),
		}
		idx[n.ID] = n
		dbg.Considered[i] = n.ID
	}

	res, err := s.llmService.VerifyRelevance(ctx, query, cands)
	if err != nil || res == nil {
		return nodes, dbg, false
	}

	keptNodes := make([]*graph.Node, 0, len(res.Keep))
	usedIDs := make(map[string]bool, len(res.Keep))
	for _, id := range res.Keep {
		if n, ok := idx[id]; ok && !usedIDs[id] {
			usedIDs[id] = true
			keptNodes = append(keptNodes, n)
			dbg.Kept = append(dbg.Kept, id)
		}
	}
	out := append(keptNodes, tail...)
	return out, dbg, true
}

// rerankWithLLM packs the head of `nodes` into RerankCandidates,
// calls the service, and rebuilds the slice in the model's order.
// Trailing candidates beyond rerankCap are kept verbatim after the
// reranked head. On any failure, returns the input unchanged.
//
// Before partitioning into head/tail, nodes are re-sorted so callable
// kinds (function / method) come before everything else — preserving
// BM25 order within each bucket. Without this, a high-scoring param
// or field node (e.g. `BM25Backend.Search#param:limit`) can pre-empt
// the enclosing method (`BM25Backend.Search`) inside the rerank
// window, leaving the model unable to surface the real callable.
func rerankWithLLM(ctx context.Context, s *Server, query string, nodes []*graph.Node) []*graph.Node {
	if s.llmService == nil || !s.llmService.Enabled() || len(nodes) < 2 {
		return nodes
	}
	nodes = prioritizeCallables(nodes)
	head := nodes
	tail := []*graph.Node(nil)
	if len(nodes) > rerankCap {
		head = nodes[:rerankCap]
		tail = nodes[rerankCap:]
	}

	cands := make([]llm.RerankCandidate, len(head))
	idx := make(map[string]*graph.Node, len(head))
	for i, n := range head {
		sig, _ := n.Meta["signature"].(string)
		cands[i] = llm.RerankCandidate{
			ID:        n.ID,
			Name:      n.Name,
			Signature: sig,
			Path:      n.FilePath,
		}
		idx[n.ID] = n
	}

	res, err := s.llmService.RerankSymbols(ctx, query, cands)
	if err != nil || res == nil || len(res.Order) == 0 {
		return nodes
	}

	reordered := make([]*graph.Node, 0, len(nodes))
	used := make(map[string]bool, len(head))
	for _, id := range res.Order {
		n, ok := idx[id]
		if !ok || used[id] {
			continue
		}
		used[id] = true
		reordered = append(reordered, n)
	}
	// Defensive: the service guarantees a permutation, but if any
	// head node is missing for any reason, append it after the
	// reranked head in its original position.
	for _, n := range head {
		if !used[n.ID] {
			reordered = append(reordered, n)
		}
	}
	reordered = append(reordered, tail...)
	return reordered
}
