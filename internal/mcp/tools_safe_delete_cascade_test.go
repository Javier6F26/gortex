package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/indexer"
	"github.com/zzet/gortex/internal/query"

	"github.com/zzet/gortex/internal/config"
)

// indexFixture indexes the supplied directory and returns the
// running Server. Reused by every cascade test that relies on the
// natural graph the indexer produces.
func indexFixture(t *testing.T, dir string) *Server {
	t.Helper()
	g := graph.New()
	reg := testRegistry()
	cfg := config.Default()
	idx := indexer.New(g, reg, cfg.Index, zap.NewNop())
	_, err := idx.Index(dir)
	require.NoError(t, err)

	eng := query.NewEngine(g)
	srv := NewServer(eng, g, idx, nil, zap.NewNop(), nil)
	srv.RunAnalysis()
	return srv
}

// callSafeDeleteCascade invokes the safe_delete_symbol handler
// directly. safe_delete_symbol is a deferred (lazy) tool, so
// MCPServer().GetTool returns nil until the tools_search promotion
// has run; the bare-handler path matches what the existing
// tools_safe_delete_test.go uses and exercises the same logic.
func callSafeDeleteCascade(t *testing.T, srv *Server, args map[string]any) *mcplib.CallToolResult {
	t.Helper()
	req := mcplib.CallToolRequest{Params: mcplib.CallToolParams{
		Name:      "safe_delete_symbol",
		Arguments: args,
	}}
	res, err := srv.handleSafeDeleteSymbol(context.Background(), req)
	require.NoError(t, err)
	return res
}

// readCascadeJSON unmarshals the tool result's first text content
// into a map for assertion.
func readCascadeJSON(t *testing.T, res *mcplib.CallToolResult) map[string]any {
	t.Helper()
	require.False(t, res.IsError, "tool returned error: %#v", res.Content)
	require.NotEmpty(t, res.Content)
	text := res.Content[0].(mcplib.TextContent).Text
	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &out))
	return out
}

// closureIDs extracts the IDs of every entry in a cascade_closure
// payload. Used for set comparison in assertions.
func closureIDs(resp map[string]any) []string {
	raw, ok := resp["cascade_closure"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if id, ok := entry["id"].(string); ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// deletedIDs extracts the cascade_deleted payload, similarly sorted.
func deletedIDs(resp map[string]any) []string {
	raw, ok := resp["cascade_deleted"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if id, ok := item.(string); ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// readSource is a small helper around os.ReadFile that returns the
// file contents as a string so callers can assertContains it.
func readSource(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

// ---------------------------------------------------------------
// Tests
// ---------------------------------------------------------------

// TestCascade_LinearChain — a → b → c, delete a with cascade:apply,
// verify b and c are also deleted from disk and the closure carries
// both IDs.
func TestCascade_LinearChain(t *testing.T) {
	dir := t.TempDir()
	src := "package chain\n\n" +
		"func a() { b() }\n\n" +
		"func b() { c() }\n\n" +
		"func c() {}\n"
	srcPath := filepath.Join(dir, "chain.go")
	require.NoError(t, os.WriteFile(srcPath, []byte(src), 0o644))

	srv := indexFixture(t, dir)

	// Sanity: all three symbols should be indexed.
	require.NotNil(t, srv.graph.GetNode("chain.go::a"))
	require.NotNil(t, srv.graph.GetNode("chain.go::b"))
	require.NotNil(t, srv.graph.GetNode("chain.go::c"))

	res := callSafeDeleteCascade(t, srv, map[string]any{
		"id":      "chain.go::a",
		"cascade": "apply",
		"dry_run": false,
	})
	resp := readCascadeJSON(t, res)
	assert.Equal(t, "deleted", resp["status"])
	assert.Equal(t, "apply", resp["cascade_mode"])

	closure := closureIDs(resp)
	assert.ElementsMatch(t, []string{"chain.go::b", "chain.go::c"}, closure,
		"closure must include b and c (transitively orphaned)")
	deleted := deletedIDs(resp)
	assert.ElementsMatch(t, []string{"chain.go::a", "chain.go::b", "chain.go::c"}, deleted,
		"cascade_deleted must list every removed symbol including target")

	out := readSource(t, srcPath)
	assert.NotContains(t, out, "func a(")
	assert.NotContains(t, out, "func b(")
	assert.NotContains(t, out, "func c(")
}

// TestCascade_Branching — a → b; a → c; d → b. Deleting a should
// drop c (only a→c) but leave b alone (d still references it).
func TestCascade_Branching(t *testing.T) {
	dir := t.TempDir()
	src := "package branch\n\n" +
		"func a() { b(); c() }\n\n" +
		"func b() {}\n\n" +
		"func c() {}\n\n" +
		"func d() { b() }\n"
	srcPath := filepath.Join(dir, "branch.go")
	require.NoError(t, os.WriteFile(srcPath, []byte(src), 0o644))

	srv := indexFixture(t, dir)

	res := callSafeDeleteCascade(t, srv, map[string]any{
		"id":      "branch.go::a",
		"cascade": "apply",
		"dry_run": false,
	})
	resp := readCascadeJSON(t, res)
	assert.Equal(t, "deleted", resp["status"])

	closure := closureIDs(resp)
	assert.Equal(t, []string{"branch.go::c"}, closure,
		"closure must contain only c — b is still used by d")

	deleted := deletedIDs(resp)
	assert.ElementsMatch(t, []string{"branch.go::a", "branch.go::c"}, deleted)

	out := readSource(t, srcPath)
	assert.NotContains(t, out, "func a(")
	assert.NotContains(t, out, "func c(")
	assert.Contains(t, out, "func b(", "b must remain — referenced by d")
	assert.Contains(t, out, "func d(", "d must remain — external to closure")
}

// TestCascade_CycleWithinClosure — two functions calling each other
// with no external callers. cascade:apply should treat the cycle as
// part of the closure (self-references inside D do not count as
// external) and delete both.
//
// The Go indexer naturally produces the mutual call edges; the only
// trick is to ensure neither function has an external caller. We
// keep a helper function in the file that calls neither so the
// package is still buildable for the indexer's purposes.
func TestCascade_CycleWithinClosure(t *testing.T) {
	dir := t.TempDir()
	src := "package cyc\n\n" +
		"func a() { b() }\n\n" +
		"func b() { a() }\n\n" +
		"func entry() {}\n"
	srcPath := filepath.Join(dir, "cyc.go")
	require.NoError(t, os.WriteFile(srcPath, []byte(src), 0o644))

	srv := indexFixture(t, dir)

	res := callSafeDeleteCascade(t, srv, map[string]any{
		"id":      "cyc.go::a",
		"cascade": "apply",
		"dry_run": false,
		// b is called from a; that single in-edge is the one we'd
		// normally guard against. force=true bypasses the initial
		// reference gate (a's in-edges from b) so the cascade pass
		// can take over.
		"force": true,
	})
	resp := readCascadeJSON(t, res)
	assert.Equal(t, "deleted", resp["status"])

	closure := closureIDs(resp)
	assert.Equal(t, []string{"cyc.go::b"}, closure,
		"cycle peer b must enter the closure even though a calls into it")

	deleted := deletedIDs(resp)
	assert.ElementsMatch(t, []string{"cyc.go::a", "cyc.go::b"}, deleted)

	out := readSource(t, srcPath)
	assert.NotContains(t, out, "func a(")
	assert.NotContains(t, out, "func b(")
	assert.Contains(t, out, "func entry(", "unrelated symbols stay")
}

// TestCascade_CrossRepoGuard — manually inject a synthetic caller
// whose WorkspaceID differs from the target's. The cross-workspace
// reference must DISQUALIFY the candidate from the closure.
func TestCascade_CrossRepoGuard(t *testing.T) {
	dir := t.TempDir()
	src := "package xrepo\n\n" +
		"func a() { b() }\n\n" +
		"func b() {}\n"
	srcPath := filepath.Join(dir, "xrepo.go")
	require.NoError(t, os.WriteFile(srcPath, []byte(src), 0o644))

	srv := indexFixture(t, dir)

	// Locate the target node and stamp a WorkspaceID so the
	// comparison is well-defined.
	aNode := srv.graph.GetNode("xrepo.go::a")
	require.NotNil(t, aNode)
	bNode := srv.graph.GetNode("xrepo.go::b")
	require.NotNil(t, bNode)
	aNode.WorkspaceID = "local"
	bNode.WorkspaceID = "local"

	// Inject a synthetic foreign caller of b in a different
	// workspace. The cascade pass must treat this as an external
	// reference and refuse to include b.
	foreignID := "ext-repo/foo.go::Caller"
	srv.graph.AddNode(&graph.Node{
		ID:          foreignID,
		Kind:        graph.KindFunction,
		Name:        "Caller",
		FilePath:    "ext-repo/foo.go",
		StartLine:   1,
		EndLine:     3,
		Language:    "go",
		RepoPrefix:  "ext-repo",
		WorkspaceID: "ext-repo",
	})
	srv.graph.AddEdge(&graph.Edge{
		From:     foreignID,
		To:       "xrepo.go::b",
		Kind:     graph.EdgeCrossRepoCalls,
		FilePath: "ext-repo/foo.go",
		Line:     2,
	})

	res := callSafeDeleteCascade(t, srv, map[string]any{
		"id":      "xrepo.go::a",
		"cascade": "apply",
		"dry_run": false,
	})
	resp := readCascadeJSON(t, res)
	assert.Equal(t, "deleted", resp["status"])

	closure := closureIDs(resp)
	assert.Empty(t, closure, "cross-workspace caller must disqualify b from the closure")

	deleted := deletedIDs(resp)
	assert.ElementsMatch(t, []string{"xrepo.go::a"}, deleted)

	out := readSource(t, srcPath)
	assert.NotContains(t, out, "func a(")
	assert.Contains(t, out, "func b(", "b must remain — it has a cross-repo caller")
}

// TestCascade_TestsOnly — a calls b; a test-file _test.go also
// calls b. By default the cascade must leave b alone (production
// stopped using it but a test still does). With
// cascade_into_tests:true the cascade must delete b along with a.
func TestCascade_TestsOnly(t *testing.T) {
	dir := t.TempDir()
	src := "package tonly\n\n" +
		"func a() { b() }\n\n" +
		"func b() {}\n"
	srcPath := filepath.Join(dir, "tonly.go")
	require.NoError(t, os.WriteFile(srcPath, []byte(src), 0o644))

	testSrc := "package tonly\n\n" +
		"import \"testing\"\n\n" +
		"func TestB(t *testing.T) { b() }\n"
	testPath := filepath.Join(dir, "tonly_test.go")
	require.NoError(t, os.WriteFile(testPath, []byte(testSrc), 0o644))

	// Case 1: default cascade — must NOT delete b.
	srv := indexFixture(t, dir)
	res := callSafeDeleteCascade(t, srv, map[string]any{
		"id":      "tonly.go::a",
		"cascade": "apply",
		"dry_run": false,
		// a has no external callers, so no force needed.
	})
	resp := readCascadeJSON(t, res)
	assert.Equal(t, "deleted", resp["status"])
	closure := closureIDs(resp)
	assert.Empty(t, closure, "test-only caller must disqualify b without cascade_into_tests")
	deleted := deletedIDs(resp)
	assert.ElementsMatch(t, []string{"tonly.go::a"}, deleted)
	out := readSource(t, srcPath)
	assert.NotContains(t, out, "func a(")
	assert.Contains(t, out, "func b(", "b must remain when only tests reference it")

	// Case 2: cascade_into_tests:true — must delete b too. Fresh
	// fixture so the file is back to its original state.
	dir2 := t.TempDir()
	srcPath2 := filepath.Join(dir2, "tonly.go")
	require.NoError(t, os.WriteFile(srcPath2, []byte(src), 0o644))
	testPath2 := filepath.Join(dir2, "tonly_test.go")
	require.NoError(t, os.WriteFile(testPath2, []byte(testSrc), 0o644))
	srv2 := indexFixture(t, dir2)

	res2 := callSafeDeleteCascade(t, srv2, map[string]any{
		"id":                 "tonly.go::a",
		"cascade":            "apply",
		"cascade_into_tests": true,
		"dry_run":            false,
	})
	resp2 := readCascadeJSON(t, res2)
	assert.Equal(t, "deleted", resp2["status"])
	closure2 := closureIDs(resp2)
	assert.Equal(t, []string{"tonly.go::b"}, closure2,
		"cascade_into_tests:true must include b in the closure")
	out2 := readSource(t, srcPath2)
	assert.NotContains(t, out2, "func b(", "b must be deleted when cascade_into_tests is true")
}

// TestCascade_PreviewMode — cascade:"preview" must report the
// closure but leave every symbol on disk untouched, including the
// target itself when dry_run=false.
func TestCascade_PreviewMode(t *testing.T) {
	dir := t.TempDir()
	src := "package prev\n\n" +
		"func a() { b() }\n\n" +
		"func b() {}\n"
	srcPath := filepath.Join(dir, "prev.go")
	require.NoError(t, os.WriteFile(srcPath, []byte(src), 0o644))

	srv := indexFixture(t, dir)

	// Preview with dry_run=true returns the planned delete but
	// makes no on-disk changes.
	res := callSafeDeleteCascade(t, srv, map[string]any{
		"id":      "prev.go::a",
		"cascade": "preview",
		"dry_run": true,
	})
	resp := readCascadeJSON(t, res)
	assert.Equal(t, "preview", resp["status"])
	assert.Equal(t, "preview", resp["cascade_mode"])
	closure := closureIDs(resp)
	assert.Equal(t, []string{"prev.go::b"}, closure,
		"preview must still compute and surface the closure")
	_, ok := resp["cascade_deleted"]
	assert.False(t, ok, "preview must not carry a cascade_deleted list")
	out := readSource(t, srcPath)
	assert.Contains(t, out, "func a(", "preview must not modify disk")
	assert.Contains(t, out, "func b(", "preview must not modify disk")

	// Commit (dry_run=false) with cascade:preview should delete
	// the target only — not the closure.
	res2 := callSafeDeleteCascade(t, srv, map[string]any{
		"id":      "prev.go::a",
		"cascade": "preview",
		"dry_run": false,
	})
	resp2 := readCascadeJSON(t, res2)
	assert.Equal(t, "deleted", resp2["status"])
	assert.Equal(t, "preview", resp2["cascade_mode"])
	closure2 := closureIDs(resp2)
	assert.Equal(t, []string{"prev.go::b"}, closure2,
		"preview mode must still report the closure on commit")
	_, ok = resp2["cascade_deleted"]
	assert.False(t, ok, "preview mode must not delete the closure even on commit")

	out2 := readSource(t, srcPath)
	assert.NotContains(t, out2, "func a(", "target itself must still be deleted")
	assert.Contains(t, out2, "func b(", "preview mode must not delete the orphan")
}

// TestCascade_BackwardCompat_Off — omitting cascade (or passing
// "off") preserves today's single-symbol delete: no closure work,
// no cascade fields in the response that change existing behaviour.
func TestCascade_BackwardCompat_Off(t *testing.T) {
	dir := t.TempDir()
	src := "package compat\n\n" +
		"func a() { b() }\n\n" +
		"func b() {}\n"
	srcPath := filepath.Join(dir, "compat.go")
	require.NoError(t, os.WriteFile(srcPath, []byte(src), 0o644))

	srv := indexFixture(t, dir)

	// cascade omitted entirely.
	res := callSafeDeleteCascade(t, srv, map[string]any{
		"id":      "compat.go::a",
		"dry_run": false,
	})
	resp := readCascadeJSON(t, res)
	assert.Equal(t, "deleted", resp["status"])
	assert.Equal(t, "off", resp["cascade_mode"], "default cascade_mode must be off")
	_, hasClosure := resp["cascade_closure"]
	assert.False(t, hasClosure, "off mode must not surface a closure list")
	_, hasDeleted := resp["cascade_deleted"]
	assert.False(t, hasDeleted, "off mode must not surface a deleted list")

	out := readSource(t, srcPath)
	assert.NotContains(t, out, "func a(", "target must still be deleted")
	assert.Contains(t, out, "func b(", "off mode leaves the orphan in place")

	// Explicit cascade:"off" behaves the same.
	dir2 := t.TempDir()
	srcPath2 := filepath.Join(dir2, "compat.go")
	require.NoError(t, os.WriteFile(srcPath2, []byte(src), 0o644))
	srv2 := indexFixture(t, dir2)

	res2 := callSafeDeleteCascade(t, srv2, map[string]any{
		"id":      "compat.go::a",
		"cascade": "off",
		"dry_run": false,
	})
	resp2 := readCascadeJSON(t, res2)
	assert.Equal(t, "deleted", resp2["status"])
	assert.Equal(t, "off", resp2["cascade_mode"])
	out2 := readSource(t, srcPath2)
	assert.NotContains(t, out2, "func a(")
	assert.Contains(t, out2, "func b(")
}

// TestCascade_NormaliseMode — unit-tests the input normaliser so the
// tool gracefully handles unknown / mixed-case input from clients.
func TestCascade_NormaliseMode(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", cascadeModeOff},
		{"off", cascadeModeOff},
		{"OFF", cascadeModeOff},
		{"preview", cascadeModePreview},
		{"Preview", cascadeModePreview},
		{" apply ", cascadeModeApply},
		{"bogus", cascadeModeOff},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, normaliseCascadeMode(tt.in),
			"normaliseCascadeMode(%q)", tt.in)
	}
}
