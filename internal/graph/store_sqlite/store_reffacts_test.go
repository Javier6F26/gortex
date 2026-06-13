package store_sqlite_test

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/graph/store_sqlite"
)

func openRefFactStore(t *testing.T) *store_sqlite.Store {
	t.Helper()
	s, err := store_sqlite.Open(filepath.Join(t.TempDir(), "rf.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRefFacts_Roundtrip(t *testing.T) {
	s := openRefFactStore(t)
	facts := []graph.RefFact{
		{RepoPrefix: "", FromID: "a.go::A", ToID: "b.go::B", Kind: "calls", RefName: "B", Line: 7, Origin: "ast_resolved", Tier: "ast", FilePath: "a.go", Lang: "go", Candidates: []string{"b.go::B", "c.go::B"}},
		{RepoPrefix: "", FromID: "a.go::A", ToID: "d.go::D", Kind: "references", RefName: "D", Line: 9, Origin: "lsp_resolved", Tier: "lsp", FilePath: "a.go", Lang: "go"},
	}
	require.NoError(t, s.BulkSetRefFacts("", facts))

	got, err := s.LoadRefFactsByFiles("", []string{"a.go"})
	require.NoError(t, err)
	require.Len(t, got, 2)

	byTo := map[string]graph.RefFact{}
	for _, f := range got {
		byTo[f.ToID] = f
	}
	require.Equal(t, "ast_resolved", byTo["b.go::B"].Origin)
	require.Equal(t, []string{"b.go::B", "c.go::B"}, byTo["b.go::B"].Candidates)
	require.Equal(t, 7, byTo["b.go::B"].Line)
	require.Equal(t, "lsp_resolved", byTo["d.go::D"].Origin)

	// LoadRefFactsByFiles with empty file list returns all for the repo.
	all, err := s.LoadRefFactsByFiles("", nil)
	require.NoError(t, err)
	require.Len(t, all, 2)
}

func TestRefFacts_DeleteByFile(t *testing.T) {
	s := openRefFactStore(t)
	require.NoError(t, s.BulkSetRefFacts("", []graph.RefFact{
		{FromID: "a.go::A", ToID: "x", Kind: "calls", FilePath: "a.go"},
		{FromID: "b.go::B", ToID: "y", Kind: "calls", FilePath: "b.go"},
	}))
	require.NoError(t, s.DeleteRefFactsByFiles("", []string{"a.go"}))
	got, err := s.LoadRefFactsByFiles("", nil)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "b.go", got[0].FilePath)
}

func TestRefFacts_RepoScoping(t *testing.T) {
	s := openRefFactStore(t)
	require.NoError(t, s.BulkSetRefFacts("repoA", []graph.RefFact{{FromID: "f::A", ToID: "tA", Kind: "calls", FilePath: "f.go"}}))
	require.NoError(t, s.BulkSetRefFacts("repoB", []graph.RefFact{{FromID: "f::A", ToID: "tB", Kind: "calls", FilePath: "f.go"}}))

	a, err := s.LoadRefFactsByFiles("repoA", []string{"f.go"})
	require.NoError(t, err)
	require.Len(t, a, 1)
	require.Equal(t, "tA", a[0].ToID)

	// Deleting repoA's file must not touch repoB.
	require.NoError(t, s.DeleteRefFactsByFiles("repoA", []string{"f.go"}))
	b, err := s.LoadRefFactsByFiles("repoB", []string{"f.go"})
	require.NoError(t, err)
	require.Len(t, b, 1)
	require.Equal(t, "tB", b[0].ToID)
}

func TestRefFacts_Chunking(t *testing.T) {
	s := openRefFactStore(t)
	const n = 500 // > refFactChunk (80)
	facts := make([]graph.RefFact, n)
	for i := range facts {
		facts[i] = graph.RefFact{FromID: fmt.Sprintf("a.go::f%d", i), ToID: fmt.Sprintf("t%d", i), Kind: "calls", FilePath: "a.go"}
	}
	require.NoError(t, s.BulkSetRefFacts("", facts))
	got, err := s.LoadRefFactsByFiles("", []string{"a.go"})
	require.NoError(t, err)
	require.Len(t, got, n)
}

func TestRefFacts_EmptyNoop(t *testing.T) {
	s := openRefFactStore(t)
	require.NoError(t, s.BulkSetRefFacts("", nil))
	require.NoError(t, s.DeleteRefFactsByFiles("", nil))
	got, err := s.LoadRefFactsByFiles("", nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestRefFacts_LoadByTargets(t *testing.T) {
	s := openRefFactStore(t)
	require.NoError(t, s.BulkSetRefFacts("", []graph.RefFact{
		{FromID: "b.go::Caller", ToID: "a.go::F", Kind: "calls", RefName: "F", Line: 3, Origin: "ast_resolved", Tier: "ast", FilePath: "b.go", Lang: "go"},
		{FromID: "c.go::Other", ToID: "a.go::F", Kind: "references", RefName: "F", FilePath: "c.go"},
		{FromID: "c.go::Other", ToID: "a.go::G", Kind: "calls", RefName: "G", FilePath: "c.go"},
		{FromID: "d.go::X", ToID: "z.go::Z", Kind: "calls", RefName: "Z", FilePath: "d.go"},
	}))

	byFile, err := s.LoadRefFactsByTargets("", []string{"a.go::F", "a.go::G"})
	require.NoError(t, err)
	require.Len(t, byFile, 2, "facts must be grouped by source file: %v", byFile)
	require.Len(t, byFile["b.go"], 1)
	require.Equal(t, "a.go::F", byFile["b.go"][0].ToID)
	require.Equal(t, "F", byFile["b.go"][0].RefName)
	require.Equal(t, "ast_resolved", byFile["b.go"][0].Origin)
	require.Len(t, byFile["c.go"], 2, "both of c.go's facts target the queried symbols")
	require.NotContains(t, byFile, "d.go", "a fact targeting an unqueried symbol must not match")
}

func TestRefFacts_LoadByTargets_EmptyAndMissing(t *testing.T) {
	s := openRefFactStore(t)
	require.NoError(t, s.BulkSetRefFacts("", []graph.RefFact{
		{FromID: "a.go::A", ToID: "b.go::B", Kind: "calls", FilePath: "a.go"},
	}))

	// Empty input: empty, non-nil map.
	empty, err := s.LoadRefFactsByTargets("", nil)
	require.NoError(t, err)
	require.NotNil(t, empty)
	require.Empty(t, empty)

	// A target nothing references: no rows, no error.
	miss, err := s.LoadRefFactsByTargets("", []string{"nope::Missing"})
	require.NoError(t, err)
	require.Empty(t, miss)
}

func TestRefFacts_LoadByTargets_RepoScoping(t *testing.T) {
	s := openRefFactStore(t)
	require.NoError(t, s.BulkSetRefFacts("repoA", []graph.RefFact{{FromID: "fa::A", ToID: "shared::T", Kind: "calls", FilePath: "fa.go"}}))
	require.NoError(t, s.BulkSetRefFacts("repoB", []graph.RefFact{{FromID: "fb::B", ToID: "shared::T", Kind: "calls", FilePath: "fb.go"}}))

	a, err := s.LoadRefFactsByTargets("repoA", []string{"shared::T"})
	require.NoError(t, err)
	require.Len(t, a, 1)
	require.Len(t, a["fa.go"], 1)
	require.Equal(t, "repoA", a["fa.go"][0].RepoPrefix, "loaded facts must carry the queried repo prefix")
	require.NotContains(t, a, "fb.go", "another repo's facts must not leak into the result")
}

func TestRefFacts_LoadByTargets_Chunking(t *testing.T) {
	s := openRefFactStore(t)
	const n = 500 // > refFactChunk (80)
	facts := make([]graph.RefFact, n)
	targets := make([]string, n)
	for i := range facts {
		facts[i] = graph.RefFact{
			FromID:   fmt.Sprintf("src%d.go::f", i),
			ToID:     fmt.Sprintf("dst.go::t%d", i),
			Kind:     "calls",
			FilePath: fmt.Sprintf("src%d.go", i),
		}
		targets[i] = fmt.Sprintf("dst.go::t%d", i)
	}
	require.NoError(t, s.BulkSetRefFacts("", facts))

	byFile, err := s.LoadRefFactsByTargets("", targets)
	require.NoError(t, err)
	require.Len(t, byFile, n, "every chunked target must come back grouped under its source file")
}
