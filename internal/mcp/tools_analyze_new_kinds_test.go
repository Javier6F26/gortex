package mcp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

// --- env_var_users ---------------------------------------------------------

func TestAnalyzeEnvVarUsers_FiltersToEnvKeys(t *testing.T) {
	srv, _ := setupTestServer(t)
	addConfigKeyNode(srv.graph, "cfg::env::DATABASE_URL", "DATABASE_URL", "env")
	addConfigKeyNode(srv.graph, "viper::log.level", "log.level", "viper")
	addReadConfigEdge(srv.graph, "f.go::A", "cfg::env::DATABASE_URL")
	addReadConfigEdge(srv.graph, "f.go::B", "cfg::env::DATABASE_URL")
	addReadConfigEdge(srv.graph, "f.go::C", "viper::log.level")

	out := callAnalyze(t, srv, "env_var_users", map[string]any{})
	rows, _ := out["env_vars"].([]any)
	require.Len(t, rows, 1, "viper (non-env) config key must be excluded")
	row := rows[0].(map[string]any)
	require.Equal(t, "DATABASE_URL", row["name"])
	require.Equal(t, float64(2), row["reads"])
}

func TestAnalyzeEnvVarUsers_NameFilter(t *testing.T) {
	srv, _ := setupTestServer(t)
	addConfigKeyNode(srv.graph, "env::PORT", "PORT", "env")
	addConfigKeyNode(srv.graph, "env::HOST", "HOST", "env")
	addReadConfigEdge(srv.graph, "f.go::A", "env::PORT")
	addReadConfigEdge(srv.graph, "f.go::B", "env::HOST")

	out := callAnalyze(t, srv, "env_var_users", map[string]any{"name": "port"})
	require.Equal(t, float64(1), out["total"])
}

// --- sql_call_sites --------------------------------------------------------

func TestAnalyzeSQLCallSites_GroupsByCaller(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.graph.AddNode(&graph.Node{ID: "f.go::GetUser", Kind: graph.KindFunction, Name: "GetUser", FilePath: "f.go"})
	srv.graph.AddNode(&graph.Node{ID: "tbl::users", Kind: graph.KindTable, Name: "users"})
	srv.graph.AddNode(&graph.Node{ID: "tbl::orders", Kind: graph.KindTable, Name: "orders"})
	srv.graph.AddEdge(&graph.Edge{From: "f.go::GetUser", To: "tbl::users", Kind: graph.EdgeQueries, Meta: map[string]any{"op": "read"}})
	srv.graph.AddEdge(&graph.Edge{From: "f.go::GetUser", To: "tbl::orders", Kind: graph.EdgeQueries, Meta: map[string]any{"op": "write"}})

	out := callAnalyze(t, srv, "sql_call_sites", map[string]any{})
	rows, _ := out["call_sites"].([]any)
	require.Len(t, rows, 1)
	row := rows[0].(map[string]any)
	require.Equal(t, "GetUser", row["name"])
	require.Equal(t, float64(2), row["queries"])
	require.Equal(t, float64(1), row["reads"])
	require.Equal(t, float64(1), row["writes"])
}

// --- fixes_history ---------------------------------------------------------

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t.test")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestFixSubjectRe(t *testing.T) {
	for _, s := range []string{"fix: x", "fixes #3", "fixed the bug", "bugfix in y", "hotfix", "FIX something"} {
		require.True(t, fixSubjectRe.MatchString(s), "%q should be a fix subject", s)
	}
	for _, s := range []string{"add feature", "prefix handling", "refactor fixtures", "update docs"} {
		require.False(t, fixSubjectRe.MatchString(s), "%q should NOT be a fix subject", s)
	}
}

func TestMineFixCommits_DetectsFixSubjects(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q")
	commit := func(body, msg string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte(body), 0o644))
		mustGit(t, dir, "add", "a.go")
		mustGit(t, dir, "commit", "-q", "-m", msg)
	}
	commit("package a\n", "add feature a")
	commit("package a\n// v2\n", "fix: nil deref in a")
	commit("package a\n// v3\n", "fixes crash on startup")

	commits := mineFixCommits(context.Background(), dir, 100)
	require.Len(t, commits, 2, "two fix commits, the plain-feature commit excluded")
	for _, c := range commits {
		require.Contains(t, c.files, "a.go")
	}
}

func TestAnalyzeFixesHistory_EndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	srv, dir := setupTestServer(t)
	mustGit(t, dir, "init", "-q")
	mustGit(t, dir, "add", "main.go")
	mustGit(t, dir, "commit", "-q", "-m", "initial commit")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"),
		[]byte("package main\n\nfunc main() {}\n"), 0o644))
	mustGit(t, dir, "add", "main.go")
	mustGit(t, dir, "commit", "-q", "-m", "fix: correct main logic")

	out := callAnalyze(t, srv, "fixes_history", map[string]any{})
	if got, _ := out["total_fix_commits"].(float64); got < 1 {
		t.Fatalf("expected >=1 fix commit, got %v", got)
	}
	files, _ := out["files"].([]any)
	require.NotEmpty(t, files)
	require.Equal(t, "main.go", files[0].(map[string]any)["file"])
}

func TestSymbolNamesInFile(t *testing.T) {
	srv, _ := setupTestServer(t)
	// setupTestServer indexes a main.go containing main + helper.
	require.NotEmpty(t, srv.symbolNamesInFile("main.go"))
}

// --- edge_audit ------------------------------------------------------------

func TestAnalyzeEdgeAudit_Buckets(t *testing.T) {
	srv, _ := setupTestServer(t)
	g := srv.graph

	// An interface with no implementor.
	g.AddNode(&graph.Node{ID: "z.go::Orphan", Kind: graph.KindInterface, Name: "Orphan"})
	// An interface with an implementor — must NOT be flagged.
	g.AddNode(&graph.Node{ID: "z.go::Used", Kind: graph.KindInterface, Name: "Used"})
	g.AddNode(&graph.Node{ID: "z.go::Impl", Kind: graph.KindType, Name: "Impl"})
	g.AddEdge(&graph.Edge{From: "z.go::Impl", To: "z.go::Used", Kind: graph.EdgeImplements})
	// A function reached only from a test file, via a text-matched edge.
	g.AddNode(&graph.Node{ID: "z.go::Helper", Kind: graph.KindFunction, Name: "Helper", FilePath: "z.go"})
	g.AddNode(&graph.Node{ID: "z_test.go::TestZ", Kind: graph.KindFunction, Name: "TestZ", FilePath: "z_test.go"})
	g.AddEdge(&graph.Edge{
		From: "z_test.go::TestZ", To: "z.go::Helper",
		Kind: graph.EdgeCalls, Origin: graph.OriginTextMatched,
	})

	out := callAnalyze(t, srv, "edge_audit", map[string]any{})
	require.Contains(t, out, "edge_tiers")
	require.Contains(t, out, "summary")

	ui := out["unimplemented_interfaces"].(map[string]any)
	require.GreaterOrEqual(t, ui["count"].(float64), float64(1))
	require.NotContains(t, sampleStrings(ui), "z.go::Used")

	to := out["test_only_targets"].(map[string]any)
	require.Contains(t, sampleStrings(to), "z.go::Helper")

	wc := out["weak_call_edges"].(map[string]any)
	require.GreaterOrEqual(t, wc["count"].(float64), float64(1))
}

func sampleStrings(bucket map[string]any) []string {
	raw, _ := bucket["sample"].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
