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

// TestTrackedFileStateMissingFile proves the three-way verdict that splits a
// deleted-on-disk file out of the binary stale check: fresh -> stale (mtime
// drift) -> missing (the file is gone). IsTrackedStale folds missing into
// not-stale, so only TrackedFileState surfaces the deletion.
func TestTrackedFileStateMissingFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.go")
	writeFile(t, target, "package a\n\nfunc A() {}\n")

	g := graph.New()
	idx := newTestIndexer(g)
	idx.SetRootPath(dir)
	_, err := idx.Index(dir)
	require.NoError(t, err)

	// Freshly indexed and untracked paths both read fresh.
	require.Equal(t, FileFresh, idx.TrackedFileState("a.go"))
	require.Equal(t, FileFresh, idx.TrackedFileState("untracked.md"))

	// mtime drift -> stale.
	future := time.Now().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(target, future, future))
	require.Equal(t, FileStale, idx.TrackedFileState("a.go"))

	// Deleted on disk -> missing, and IsTrackedStale must NOT report it as
	// stale (the distinction the freshness rider relies on).
	require.NoError(t, os.Remove(target))
	require.Equal(t, FileMissing, idx.TrackedFileState("a.go"))
	require.False(t, idx.IsTrackedStale("a.go"),
		"a deleted file is missing, not stale — IsTrackedStale folds it into not-stale")
}
