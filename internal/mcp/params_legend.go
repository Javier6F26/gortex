package mcp

import (
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// sharedParamLegend documents the parameters that recur across most
// list-shaped tools ONCE, in the MCP server `instructions` (emitted per
// session at initialize). Per-tool schemas then carry a terse one-liner
// per shared parameter instead of repeating the same paragraph 10-50
// times — the single largest source of duplicated schema bytes in the
// cold tools/list. Every shared parameter still exists on every tool that
// accepted it; only its prose moved here.
const sharedParamLegend = `Shared parameters (recur across list-shaped tools; per-tool schemas keep a one-line gloss):
- format: wire format for the response — json (verbose default), gcx (GCX1, round-trippable, ~27% fewer tokens; the default for known coding-agent clients), or toon (compact tabular text, lossy). An explicit value always wins.
- max_bytes: cap the marshalled response at this many bytes; the longest list is trimmed and truncation metadata (_truncated_by_budget, _max_returned_*) rides on the response. Omit or 0 = no cap; pass a value to opt into a tighter budget, or max_bytes:0 to opt out entirely.
- cursor: opaque pagination token from a previous response's next_cursor; round-trip it verbatim to fetch the next page (do not parse it).
- fields: comma-separated sparse fieldset — keep only these columns on each result row. Pure size win, no priority drops.
- limit: maximum rows to return (per-tool default, usually 20-50). Prefer pagination over a very large limit.
- scope: the slug of a saved scope (see save_scope) whose repositories and paths narrow the matches.
- repo / project / workspace / ref: multi-repo filters — a repository prefix/path, a project name, a workspace slug (session-bound sessions may only name their own), or a reference tag. Default to the session's cwd-bound repo.`

// sharedParamRewrites maps a recurring parameter name to the terse gloss
// that replaces its verbose per-tool description once the full semantics
// live in the shared legend. A rewrite fires only when the existing
// description carries one of the discriminator tokens, so a same-named but
// semantically-different parameter (e.g. a diff `scope` of
// unstaged/staged/all, or the nav `cursor` = focused symbol) keeps its own
// prose. Order is irrelevant — each parameter is matched independently.
var sharedParamRewrites = []struct {
	name    string
	mustHave []string // lower-cased discriminator tokens; any match qualifies
	terse   string
}{
	{"format", []string{"gcx", "toon", "wire format"}, "Wire format: json|gcx|toon; see server instructions."},
	{"max_bytes", []string{"byte"}, "Response byte cap (0/omit = none); see server instructions."},
	{"cursor", []string{"pagination", "next_cursor", "next page"}, "Opaque pagination cursor from a prior next_cursor; see server instructions."},
	{"fields", []string{"comma-separated"}, "Comma-separated sparse fieldset to keep; see server instructions."},
	{"scope", []string{"saved scope"}, "Saved-scope slug narrowing repos/paths; see server instructions."},
	{"repo", []string{"repositor"}, "Repository prefix/path filter (multi-repo); see server instructions."},
	{"project", []string{"project"}, "Restrict to repositories in a project; see server instructions."},
	{"workspace", []string{"workspace"}, "Workspace slug filter (session-bound); see server instructions."},
	{"ref", []string{"reference"}, "Reference-tag repository filter; see server instructions."},
}

// agentParamCap is the target length for a parameter description on the
// lean `agent` surface. Above it, leanGloss keeps the first sentence (or a
// word-boundary cut) so the coding-agent tools/list stays inside its byte
// ceiling. The full prose is always one tools_search / `full` preset away.
const agentParamCap = 40

// leanGloss compacts a parameter description for the lean agent surface:
// keep a leading "(qualifier)" if present (it disambiguates kind-specific
// params), then the first sentence, capped at agentParamCap.
func leanGloss(desc string) string {
	desc = strings.TrimSpace(desc)
	if len(desc) <= agentParamCap {
		return desc
	}
	if i := strings.IndexByte(desc, '.'); i > 0 && i+1 <= agentParamCap {
		return desc[:i+1]
	}
	cut := agentParamCap
	if sp := strings.LastIndexByte(desc[:cut], ' '); sp > agentParamCap/2 {
		cut = sp
	}
	return strings.TrimRight(desc[:cut], " ,;:—-") + "…"
}

// agentDescCap caps a tool DESCRIPTION on the lean surface to its leading
// contract sentences, dropping trailing reference detail / examples (which
// stay in the `full` preset and tools_search). Kept generous (~5 sentences)
// so the "what it does + key caveat" contract + trust content survives —
// only the longest descriptions' reference tails are trimmed. 0 disables.
const agentDescCap = 600

// leanDescription keeps the leading sentences of a tool description up to
// agentDescCap, cutting on a sentence boundary so the contract stays whole.
func leanDescription(desc string) string {
	if agentDescCap <= 0 || len(desc) <= agentDescCap {
		return desc
	}
	cut := agentDescCap
	if i := strings.LastIndex(desc[:cut], ". "); i > agentDescCap/2 {
		return desc[:i+1]
	}
	if sp := strings.LastIndexByte(desc[:cut], ' '); sp > agentDescCap/2 {
		cut = sp
	}
	return strings.TrimRight(desc[:cut], " ,;:—-") + "…"
}

// leanizeAgentTool returns a copy of the tool with every parameter
// description compacted to a lean gloss (and, when agentDescCap is set, the
// tool description trimmed to its leading contract sentences). It deep-
// copies the properties map (and each property object) so the server's
// shared, registered schema is never mutated — only this session's
// tools/list view is compacted.
func leanizeAgentTool(t mcp.Tool) mcp.Tool {
	t.Description = leanDescription(t.Description)
	props := t.InputSchema.Properties
	if len(props) == 0 {
		return t
	}
	out := make(map[string]any, len(props))
	for name, raw := range props {
		pm, ok := raw.(map[string]any)
		if !ok {
			out[name] = raw
			continue
		}
		cp := make(map[string]any, len(pm))
		for k, v := range pm {
			cp[k] = v
		}
		if d, ok := cp["description"].(string); ok {
			cp["description"] = leanGloss(d)
		}
		out[name] = cp
	}
	t.InputSchema.Properties = out
	return t
}

// leanizeAgentTools maps leanizeAgentTool over a slice.
func leanizeAgentTools(tools []mcp.Tool) []mcp.Tool {
	out := make([]mcp.Tool, len(tools))
	for i := range tools {
		out[i] = leanizeAgentTool(tools[i])
	}
	return out
}

// compactSharedToolParams rewrites the recurring-parameter descriptions on
// one tool to their terse gloss, having moved the full semantics into the
// shared legend. Idempotent, allocation-light, and a no-op for any tool
// whose parameters don't match a discriminator — so bespoke same-named
// parameters are left untouched. Applied at registration to every tool, so
// the saving compounds across the whole surface (core and full shrink too).
func compactSharedToolParams(tool *mcp.Tool) {
	props := tool.InputSchema.Properties
	if len(props) == 0 {
		return
	}
	for _, rw := range sharedParamRewrites {
		raw, ok := props[rw.name]
		if !ok {
			continue
		}
		pm, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		desc, _ := pm["description"].(string)
		// Never inflate: some tools already carry a terse gloss shorter than
		// the shared one-liner (e.g. a bare "json|gcx|toon" format hint).
		if desc == "" || len(rw.terse) >= len(desc) {
			continue
		}
		low := strings.ToLower(desc)
		matched := false
		for _, tok := range rw.mustHave {
			if strings.Contains(low, tok) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		pm["description"] = rw.terse
	}
}
