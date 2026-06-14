package indexer

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

// TestStaleFilesAffectDerivedEdges verifies the doc/config fast-path guard:
// a code file affects the capability/dispatch synthesizers, a doc-only file
// does not, so a README edit can skip those whole-graph passes.
func TestStaleFilesAffectDerivedEdges(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package a\n\nfunc A() {}\n")
	writeFile(t, filepath.Join(dir, "README.md"), "# Title\n\nsome prose\n")

	g := graph.New()
	idx := newTestIndexer(g)
	idx.SetRootPath(dir)
	_, err := idx.Index(dir)
	require.NoError(t, err)

	require.True(t, idx.staleFilesAffectDerivedEdges([]string{filepath.Join(dir, "a.go")}),
		"a .go file with a function must affect derived edges")
	require.False(t, idx.staleFilesAffectDerivedEdges([]string{filepath.Join(dir, "README.md")}),
		"a doc-only change must not affect capability/dispatch edges")
	require.False(t, idx.staleFilesAffectDerivedEdges(nil),
		"an empty stale set affects nothing")
}
