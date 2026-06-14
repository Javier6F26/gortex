package indexer

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

func TestIsTrackedStale(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package a\n\nfunc A() {}\n")

	g := graph.New()
	idx := newTestIndexer(g)
	idx.SetRootPath(dir)
	_, err := idx.Index(dir)
	require.NoError(t, err)

	// Freshly indexed: not stale. Unknown / untracked paths are never
	// tracked-stale (the key difference from IsStale, which treats an
	// unknown file as stale).
	require.False(t, idx.IsTrackedStale("a.go"))
	require.False(t, idx.IsTrackedStale("does_not_exist.go"))
	require.False(t, idx.IsTrackedStale("untracked.md"))

	// Touch the file with a later mtime: now tracked-stale.
	future := time.Now().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(filepath.Join(dir, "a.go"), future, future))
	require.True(t, idx.IsTrackedStale("a.go"))
}
