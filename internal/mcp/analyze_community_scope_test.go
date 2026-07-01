package mcp

// Tests for the workspace clamp on the community / synthesizer analyze
// kinds. These kinds run a global graph algorithm over the whole index
// (one shared partition / one edge sweep), so without a clamp a
// workspace-bound caller would see results whose members live in a
// sibling workspace — a breach of the hard workspace isolation boundary.
// The "repo-a" / "repo-b" substring probe mirrors analyze_scope_test.go:
// multi-repo node IDs and file paths are repo-prefixed, so a result
// scoped to ws-a must never contain the string "repo-b".

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAnalyzeScope_Clusters_WorkspaceClamp(t *testing.T) {
	// repo-a and repo-b sit in SEPARATE workspaces, so a session bound to
	// one must never surface the other's community.
	srv, paths := newAnalyzeServer(t, true,
		analyzeRepoSpec{name: "repo-a", workspace: "ws-a", body: structuralBody("a")},
		analyzeRepoSpec{name: "repo-b", workspace: "ws-b", body: structuralBody("b")},
	)

	// Bound to repo-a (workspace ws-a): must show repo-a, never leak
	// repo-b's sibling-workspace community.
	ctxA := sessionCtx("s-a", paths["repo-a"])
	textA, applied, _ := runAnalyze(t, srv, ctxA, map[string]any{"kind": "clusters"})
	assert.Contains(t, textA, "repo-a", "clusters in ws-a must include repo-a's own community")
	assert.NotContains(t, textA, "repo-b", "clusters in ws-a must not leak repo-b (sibling workspace)")
	assert.Equal(t, "workspace", applied)
	// The applied scope is disclosed in the response BODY, not just _meta
	// (which clients such as Claude Code do not surface).
	assert.Contains(t, textA, "workspace:ws-a", "clusters must disclose the applied workspace scope in the body")

	// Bound to repo-b: the global partition genuinely contains repo-b's
	// community (so the probe above is not vacuous), and repo-a must not
	// leak the other way.
	ctxB := sessionCtx("s-b", paths["repo-b"])
	textB, _, _ := runAnalyze(t, srv, ctxB, map[string]any{"kind": "clusters"})
	assert.Contains(t, textB, "repo-b", "clusters in ws-b must include repo-b's own community")
	assert.NotContains(t, textB, "repo-a", "clusters in ws-b must not leak repo-a (sibling workspace)")

	// A repo narrowing arg on a kind that is workspace-bound but not
	// repo-narrowed in v1 self-discloses the widening in the body.
	textNote, _, _ := runAnalyze(t, srv, ctxA, map[string]any{"kind": "clusters", "repo": "repo-a"})
	assert.Contains(t, textNote, "not repo/project-narrowed",
		"a narrowing arg on clusters must self-disclose the v1 no-op in the body")
}
