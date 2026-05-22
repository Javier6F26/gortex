package indexer

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/search"
)

// inertTestWatcher builds an indexed repo with a single Go file and a
// Watcher wired to it, ready to drive patchGraph directly. The search
// backend is set because the modify path evicts from and adds to it.
func inertTestWatcher(t *testing.T, fileName, content string) (string, *Indexer, *Watcher) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	writeTestFile(t, path, content)

	g := graph.New()
	idx := newTestIndexer(g)
	idx.search = search.NewBM25()
	idx.SetRootPath(dir)
	_, err := idx.Index(dir)
	require.NoError(t, err)

	w, err := NewWatcher(idx, config.WatchConfig{Enabled: true, DebounceMs: 10}, zap.NewNop())
	require.NoError(t, err)
	return dir, idx, w
}

// TestWatcher_CommentOnlyChangeSkipsReindex is the central proof of
// the content-aware skip: a save that adds only comment lines — which
// shifts every following line number but changes no Function / Type /
// Method — must not trigger a structural reindex. The proof is node
// pointer identity: a reindex evicts and rebuilds the file's nodes,
// minting fresh structs, so if the live node pointer is unchanged
// after the save, no eviction happened.
func TestWatcher_CommentOnlyChangeSkipsReindex(t *testing.T) {
	dir, idx, w := inertTestWatcher(t, "main.go", `package main

func Stable() {}
`)
	path := filepath.Join(dir, "main.go")

	nodes := idx.graph.GetFileNodes("main.go")
	var funcID string
	for _, n := range nodes {
		if n.Kind == graph.KindFunction && n.Name == "Stable" {
			funcID = n.ID
		}
	}
	require.NotEmpty(t, funcID, "Stable must be indexed before the edit")
	before := idx.graph.GetNode(funcID)
	require.NotNil(t, before)

	// Comment-only edit: the function and its signature are untouched,
	// but every line below the new comments shifts down.
	writeTestFile(t, path, `package main

// Stable does nothing at all.
// This comment block is brand new and shifts the line numbers.
func Stable() {}
`)
	w.patchGraph(path, ChangeModified)

	after := idx.graph.GetNode(funcID)
	require.NotNil(t, after, "the function node must still exist")
	assert.Same(t, before, after,
		"a comment-only change must not evict and rebuild the node — "+
			"the structural reindex was supposed to be skipped")
}

// TestWatcher_WhitespaceChangeSkipsReindex proves a pure whitespace
// reflow — blank lines, indentation — is treated as structurally
// inert. Like the comment case, the node pointer must survive.
func TestWatcher_WhitespaceChangeSkipsReindex(t *testing.T) {
	dir, idx, w := inertTestWatcher(t, "main.go", `package main

func KeepMe() {}
`)
	path := filepath.Join(dir, "main.go")

	before := nodeByName(t, idx, "main.go", "KeepMe")

	// Whitespace-only edit: extra blank lines, no structural change.
	writeTestFile(t, path, `package main



func KeepMe() {}


`)
	w.patchGraph(path, ChangeModified)

	after := nodeByName(t, idx, "main.go", "KeepMe")
	assert.Same(t, before, after,
		"a whitespace-only change must skip the structural reindex")
}

// TestWatcher_InertChangeEmitsZeroDeltaEvent checks the skip path
// still reports the save through the events channel and history, but
// with every node/edge delta zero — nothing structural moved.
func TestWatcher_InertChangeEmitsZeroDeltaEvent(t *testing.T) {
	dir, _, w := inertTestWatcher(t, "main.go", `package main

func Same() {}
`)
	path := filepath.Join(dir, "main.go")

	writeTestFile(t, path, `package main

// freshly added doc line
func Same() {}
`)
	w.patchGraph(path, ChangeModified)

	select {
	case ev := <-w.Events():
		assert.Equal(t, ChangeModified, ev.Kind)
		assert.Zero(t, ev.NodesAdded, "an inert skip adds no nodes")
		assert.Zero(t, ev.NodesRemoved, "an inert skip removes no nodes")
		assert.Zero(t, ev.EdgesAdded, "an inert skip adds no edges")
		assert.Zero(t, ev.EdgesRemoved, "an inert skip removes no edges")
	default:
		t.Fatal("an inert modify must still publish a change event")
	}

	history := w.History()
	require.Len(t, history, 1)
	assert.Equal(t, ChangeModified, history[0].Kind)
}

// TestWatcher_InertChangeFiresCallbackWithUnchangedSymbols verifies
// the symbol-change callback still fires on a skipped save, with the
// before and after symbol sets identical — consumers see a coherent
// no-op rather than no callback at all.
func TestWatcher_InertChangeFiresCallbackWithUnchangedSymbols(t *testing.T) {
	dir, _, w := inertTestWatcher(t, "main.go", `package main

func Untouched() {}
`)
	path := filepath.Join(dir, "main.go")

	var mu sync.Mutex
	var gotOld, gotNew []string
	calls := 0
	w.OnSymbolChange(func(_ string, oldSymbols, newSymbols []*graph.Node) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		for _, n := range oldSymbols {
			gotOld = append(gotOld, n.Name)
		}
		for _, n := range newSymbols {
			gotNew = append(gotNew, n.Name)
		}
	})

	writeTestFile(t, path, `package main

// a comment, nothing else
func Untouched() {}
`)
	w.patchGraph(path, ChangeModified)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, calls, "the callback must fire once on an inert save")
	assert.Equal(t, gotOld, gotNew,
		"an inert save must report identical before/after symbol sets")
	assert.Contains(t, gotOld, "Untouched")
}

// TestWatcher_StructuralChangeStillReindexes is the other half of the
// contract: a save that DOES change a structural symbol must reindex
// normally. Renaming the function changes the fingerprint, so the
// skip must not engage — the old symbol is evicted and the new one
// indexed.
func TestWatcher_StructuralChangeStillReindexes(t *testing.T) {
	dir, idx, w := inertTestWatcher(t, "main.go", `package main

func OldName() {}
`)
	path := filepath.Join(dir, "main.go")
	require.NotEmpty(t, idx.graph.FindNodesByName("OldName"))

	writeTestFile(t, path, `package main

func NewName() {}
`)
	w.patchGraph(path, ChangeModified)

	assert.Empty(t, idx.graph.FindNodesByName("OldName"),
		"a renamed function must be evicted — the reindex must run")
	assert.NotEmpty(t, idx.graph.FindNodesByName("NewName"),
		"the renamed function must be indexed under its new name")

	select {
	case ev := <-w.Events():
		assert.Equal(t, ChangeModified, ev.Kind)
		assert.NotZero(t, ev.NodesRemoved,
			"a structural change must report evicted nodes")
	default:
		t.Fatal("a structural modify must publish a change event")
	}
}

// TestWatcher_AddedSymbolStillReindexes covers the additive case:
// declaring a new function alongside the existing one changes the
// fingerprint (a new tuple appears), so the reindex must run.
func TestWatcher_AddedSymbolStillReindexes(t *testing.T) {
	dir, idx, w := inertTestWatcher(t, "main.go", `package main

func First() {}
`)
	path := filepath.Join(dir, "main.go")

	writeTestFile(t, path, `package main

func First() {}

func Second() {}
`)
	w.patchGraph(path, ChangeModified)

	assert.NotEmpty(t, idx.graph.FindNodesByName("First"))
	assert.NotEmpty(t, idx.graph.FindNodesByName("Second"),
		"adding a function must trigger a reindex so the new symbol appears")
}

// TestWatcher_SignatureChangeStillReindexes proves the fingerprint is
// signature-sensitive, not just name-sensitive: changing a function's
// parameter list keeps its name but changes its signature, and that
// must still reindex.
func TestWatcher_SignatureChangeStillReindexes(t *testing.T) {
	dir, idx, w := inertTestWatcher(t, "main.go", `package main

func Compute(a int) {}
`)
	path := filepath.Join(dir, "main.go")
	funcBefore := nodeByName(t, idx, "main.go", "Compute")

	// Same name, different signature — a structural change.
	writeTestFile(t, path, `package main

func Compute(a int, b string) {}
`)
	w.patchGraph(path, ChangeModified)

	funcAfter := nodeByName(t, idx, "main.go", "Compute")
	assert.NotSame(t, funcBefore, funcAfter,
		"a signature change must evict and rebuild the node — reindex must run")
}

// TestWatcher_InertSkipRefreshesMtime checks the skip path advances
// the indexer's recorded mtime past the save, so the adaptive poller
// does not keep re-flagging the structurally-untouched file as stale.
func TestWatcher_InertSkipRefreshesMtime(t *testing.T) {
	dir, idx, w := inertTestWatcher(t, "main.go", `package main

func Steady() {}
`)
	path := filepath.Join(dir, "main.go")

	mtimeBefore := idx.FileMtimes()["main.go"]
	require.NotZero(t, mtimeBefore)

	// Save the file with a strictly later mtime, comment-only.
	future := time.Now().Add(2 * time.Second)
	writeTestFile(t, path, `package main

// a new comment
func Steady() {}
`)
	require.NoError(t, os.Chtimes(path, future, future))
	w.patchGraph(path, ChangeModified)

	mtimeAfter := idx.FileMtimes()["main.go"]
	assert.Greater(t, mtimeAfter, mtimeBefore,
		"an inert skip must restamp the recorded mtime past the save")
}

// nodeByName returns the live graph node for a named symbol in a file,
// failing the test if it is absent.
func nodeByName(t *testing.T, idx *Indexer, relPath, name string) *graph.Node {
	t.Helper()
	for _, n := range idx.graph.GetFileNodes(relPath) {
		if n.Name == name {
			return n
		}
	}
	t.Fatalf("node %q not found in %s", name, relPath)
	return nil
}
