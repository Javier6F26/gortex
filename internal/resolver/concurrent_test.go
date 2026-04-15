package resolver

import (
	"sync"
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

// TestResolver_ConcurrentResolveFile guards against the daemon-crashing
// "concurrent map writes" panic in buildDirIndexes — two file-watcher
// debounce goroutines firing on the same per-repo Indexer both call
// Resolver.ResolveFile, both reset the dirIndex/lastDirIndex fields,
// fatal-error the runtime. Run under `go test -race` for full
// detection; the runtime fatal still triggers without -race when the
// scheduler interleaves the resets exactly.
func TestResolver_ConcurrentResolveFile(t *testing.T) {
	g := buildSmallGraph(t)
	r := New(g)

	const goroutines = 16
	const itersEach = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < itersEach; j++ {
				_ = r.ResolveFile("a.go")
			}
		}()
	}
	wg.Wait()
}

// TestCrossRepoResolver_ConcurrentResolveForRepo locks in the same
// guarantee for the multi-repo resolver. MultiWatcher fires per-repo,
// so concurrent ResolveForRepo calls on different prefixes are normal
// and must not race on the shared dirIndex maps.
func TestCrossRepoResolver_ConcurrentResolveForRepo(t *testing.T) {
	g := buildSmallGraph(t)
	cr := NewCrossRepo(g)

	const goroutines = 16
	const itersEach = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < itersEach; j++ {
				_ = cr.ResolveForRepo("repo-a")
				_ = cr.ResolveAll()
			}
		}()
	}
	wg.Wait()
}

// buildSmallGraph populates a graph with a handful of file nodes plus
// one unresolved edge so the resolver actually has work to do during
// the race test. The shape doesn't matter — only that buildDirIndexes
// observes >0 file nodes and the resolveEdge inner loop runs.
func buildSmallGraph(t *testing.T) *graph.Graph {
	t.Helper()
	g := graph.New()
	for _, fp := range []string{"repo-a/lib/a.go", "repo-a/lib/b.go", "repo-b/main.go"} {
		g.AddNode(&graph.Node{
			ID:         fp,
			Kind:       graph.KindFile,
			Name:       fp,
			FilePath:   fp,
			RepoPrefix: firstSegment(fp),
		})
	}
	g.AddNode(&graph.Node{
		ID:         "a.go",
		Kind:       graph.KindFunction,
		Name:       "Foo",
		FilePath:   "a.go",
		RepoPrefix: "repo-a",
	})
	g.AddEdge(&graph.Edge{
		From: "a.go",
		To:   "unresolved::Bar",
		Kind: graph.EdgeCalls,
	})
	return g
}

func firstSegment(p string) string {
	for i, c := range p {
		if c == '/' {
			return p[:i]
		}
	}
	return p
}
