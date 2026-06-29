package resolver

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

// TestResolveAll_ConcurrentEdits is the safety gate for the chunked master
// (same-repo) resolve. It runs Resolver.ResolveAll while an editor goroutine
// evicts and re-indexes caller files under the same resolve mutex an
// interactive edit takes — the interleaving the chunked path enables. Without
// the apply / guardCrossPackageCallEdges liveness guards this corrupts the
// graph (ReindexEdge half-resurrects an evicted edge and later panics with an
// index-out-of-range during eviction). Run with -race -count=N.
func TestResolveAll_ConcurrentEdits(t *testing.T) {
	const (
		files          = 40
		callersPerFile = 600 // 24000 pending -> ~12 chunks at the default 2048
	)
	g := graph.New()
	callerFile := func(k int) string { return fmt.Sprintf("r/a%d.go", k) }

	// Same-repo targets, one unique Helper per (file,caller) slot.
	for k := 0; k < files; k++ {
		for i := 0; i < callersPerFile; i++ {
			g.AddNode(&graph.Node{
				ID: fmt.Sprintf("r/b%d_%d.go::Helper%d_%d", k, i, k, i), Kind: graph.KindFunction,
				Name: fmt.Sprintf("Helper%d_%d", k, i), FilePath: fmt.Sprintf("r/b%d_%d.go", k, i),
				Language: "go", RepoPrefix: "r",
			})
		}
	}

	addCallerFile := func(k int) {
		for i := 0; i < callersPerFile; i++ {
			g.AddNode(&graph.Node{
				ID: fmt.Sprintf("%s::Caller%d_%d", callerFile(k), k, i), Kind: graph.KindFunction,
				Name: fmt.Sprintf("Caller%d_%d", k, i), FilePath: callerFile(k), Language: "go", RepoPrefix: "r",
			})
			g.AddEdge(&graph.Edge{
				From: fmt.Sprintf("%s::Caller%d_%d", callerFile(k), k, i),
				To:   fmt.Sprintf("unresolved::Helper%d_%d", k, i),
				Kind: graph.EdgeCalls, FilePath: callerFile(k), Line: i + 1,
			})
		}
	}
	for k := 0; k < files; k++ {
		addCallerFile(k)
	}

	var resolveDone atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		New(g).ResolveAll()
		resolveDone.Store(true)
	}()

	mu := g.ResolveMutex()
	var edits int
	for k := 0; !resolveDone.Load(); k = (k + 1) % files {
		mu.Lock()
		g.EvictFile(callerFile(k))
		addCallerFile(k)
		mu.Unlock()
		edits++
		runtime.Gosched()
	}
	wg.Wait()

	require.Greater(t, edits, 0, "editor never interleaved — increase the work size")

	for _, e := range g.AllEdges() {
		if e == nil || strings.HasPrefix(e.To, "unresolved::") || isSyntheticResolveTarget(e.To) {
			continue
		}
		require.NotNilf(t, g.GetNode(e.To),
			"edge %s -> %s resolved to a node not in the graph (dangling)", e.From, e.To)
	}
}
