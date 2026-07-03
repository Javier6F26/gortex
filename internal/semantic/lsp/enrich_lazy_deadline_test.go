package lsp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/semantic"
)

// TestLSP_Enrich_LazyDeadlineUsesUnstampedCandidateCount verifies that the
// per-repo deadline is sized from the post-filter candidate set — the symbols
// a prior pass has NOT already stamped — rather than the whole-repo node count.
// On a warm restart most nodes are already stamped, so the policy must be
// called with the small unstamped count, and BudgetSeconds must reflect it.
func TestLSP_Enrich_LazyDeadlineUsesUnstampedCandidateCount(t *testing.T) {
	repoRoot := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(repoRoot, "main.go"),
		[]byte("package main\n\nfunc A() {}\nfunc B() {}\nfunc C() {}\nfunc D() {}\nfunc E() {}\n"),
		0o644,
	))

	server := newFakeLSPServer()
	server.handle("textDocument/hover", func(_ json.RawMessage) (any, *jsonRPCError) {
		return map[string]any{
			"contents": map[string]any{"kind": "plaintext", "value": "func X()"},
		}, nil
	})

	p, cleanup := providerWithFakeServer(t, server, []string{"go"})
	defer cleanup()

	g := graph.New()
	// Five go symbols; three already carry a semantic_type stamp (a prior
	// enrichment pass) and must be skipped. Only the two unstamped ones are
	// hover candidates, so that is the count the deadline scales on.
	for i, name := range []string{"A", "B", "C", "D", "E"} {
		n := &graph.Node{
			ID: "main.go::" + name, Kind: graph.KindFunction, Name: name,
			FilePath: "main.go", StartLine: 3 + i, EndLine: 3 + i, Language: "go",
		}
		if i < 3 {
			n.Meta = map[string]any{"semantic_type": "func()"}
		}
		g.AddNode(n)
	}

	var gotCandidates int
	var policyCalled bool
	policy := func(candidates int) time.Duration {
		policyCalled = true
		gotCandidates = candidates
		// A generous fixed window so the fast fake pass never gets cut.
		return 30 * time.Second
	}

	done := make(chan struct{})
	var result *semantic.EnrichResult
	var err error
	go func() {
		defer close(done)
		result, err = p.EnrichRepoContext(context.Background(), g, "", repoRoot, policy)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("EnrichRepoContext did not return")
	}
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.True(t, policyCalled, "the deadline policy must be consulted after candidate selection")
	assert.Equal(t, 2, gotCandidates, "the policy must see the unstamped candidate count, not the whole-repo node count")
	assert.Equal(t, 2, result.HoverCandidates)
	assert.InDelta(t, (30 * time.Second).Seconds(), result.BudgetSeconds, 0.001,
		"the derived deadline must be recorded on the result for the status surface")
}

// TestLSP_Enrich_NilDeadlineRunsUnbounded verifies the un-contexted entry
// points (Enrich / EnrichRepo pass a nil policy): no context deadline is
// imposed and BudgetSeconds stays zero.
func TestLSP_Enrich_NilDeadlineRunsUnbounded(t *testing.T) {
	repoRoot := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(repoRoot, "main.go"),
		[]byte("package main\n\nfunc F() string { return \"hi\" }\n"),
		0o644,
	))

	server := newFakeLSPServer()
	server.handle("textDocument/hover", func(_ json.RawMessage) (any, *jsonRPCError) {
		return map[string]any{
			"contents": map[string]any{"kind": "plaintext", "value": "func F() string"},
		}, nil
	})

	p, cleanup := providerWithFakeServer(t, server, []string{"go"})
	defer cleanup()

	g := graph.New()
	g.AddNode(&graph.Node{
		ID: "main.go::F", Kind: graph.KindFunction, Name: "F",
		FilePath: "main.go", StartLine: 3, EndLine: 3, Language: "go",
	})

	done := make(chan struct{})
	var result *semantic.EnrichResult
	var err error
	go func() {
		defer close(done)
		result, err = p.EnrichRepoContext(context.Background(), g, "", repoRoot, nil)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("EnrichRepoContext did not return")
	}
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Zero(t, result.BudgetSeconds, "a nil policy must leave the pass unbounded")
	assert.Equal(t, 1, result.HoverCandidates)
}
