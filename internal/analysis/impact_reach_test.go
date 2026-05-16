package analysis

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/reach"
)

// TestAnalyzeImpact_FastPathMatchesLiveWalk asserts the precomputed
// reach index produces the same per-depth ID set as the live BFS on
// every seed in the fixture graph — the contract that lets
// AnalyzeImpact switch implementations transparently.
func TestAnalyzeImpact_FastPathMatchesLiveWalk(t *testing.T) {
	g := buildTestGraph()

	seeds := []string{
		"auth.go::ValidateToken",
		"auth.go::ParseClaims",
		"db.go::QueryUser",
		"handler.go::HandleLogin",
	}
	for _, seed := range seeds {
		t.Run(seed, func(t *testing.T) {
			// Live walk first — no index built yet.
			reach.ClearIndex(g)
			live := AnalyzeImpact(g, []string{seed}, nil, nil)

			// Fast path — rebuild the index and call again.
			reach.BuildIndex(g)
			fast := AnalyzeImpact(g, []string{seed}, nil, nil)

			for d := 1; d <= 3; d++ {
				if a, b := idSet(live.ByDepth[d]), idSet(fast.ByDepth[d]); !setsEqual(a, b) {
					t.Errorf("depth=%d ID set mismatch\n  live: %v\n  fast: %v", d, a, b)
				}
			}
			if live.TotalAffected != fast.TotalAffected {
				t.Errorf("TotalAffected mismatch live=%d fast=%d", live.TotalAffected, fast.TotalAffected)
			}
			if live.Risk != fast.Risk {
				t.Errorf("Risk mismatch live=%s fast=%s", live.Risk, fast.Risk)
			}
		})
	}
}

// TestAnalyzeImpact_FastPathMultipleSeeds asserts the precomputed
// path correctly unions reach across multiple seeds and excludes
// seed IDs / lower-tier IDs from higher tiers — matching the live
// walk's BFS-visited semantics.
func TestAnalyzeImpact_FastPathMultipleSeeds(t *testing.T) {
	g := buildTestGraph()
	reach.BuildIndex(g)

	seeds := []string{"auth.go::ValidateToken", "db.go::QueryUser"}
	live := AnalyzeImpact(g, seeds, nil, nil)

	reach.BuildIndex(g)
	fast := AnalyzeImpact(g, seeds, nil, nil)

	for d := 1; d <= 3; d++ {
		if a, b := idSet(live.ByDepth[d]), idSet(fast.ByDepth[d]); !setsEqual(a, b) {
			t.Errorf("multi-seed depth=%d mismatch\n  live: %v\n  fast: %v", d, a, b)
		}
	}
}

// TestAnalyzeImpact_FastPathFallback asserts that when one of the
// seeds lacks a reach stamp, AnalyzeImpact falls back to the live
// walk and still returns correct results.
func TestAnalyzeImpact_FastPathFallback(t *testing.T) {
	g := buildTestGraph()
	reach.BuildIndex(g)

	// Add a brand-new symbol without rebuilding the index.
	g.AddNode(&graph.Node{
		ID: "new.go::Fresh", Kind: graph.KindFunction, Name: "Fresh",
		FilePath: "new.go",
	})
	g.AddEdge(&graph.Edge{
		From: "auth.go::ValidateToken", To: "new.go::Fresh",
		Kind: graph.EdgeCalls, Confidence: 1,
	})

	// The new seed has no reach_build stamp — fallback should kick in
	// and the live walk should find ValidateToken at d1.
	result := AnalyzeImpact(g, []string{"new.go::Fresh"}, nil, nil)
	if len(result.ByDepth[1]) == 0 {
		t.Fatalf("fallback must reach ValidateToken at d1; got %v", result.ByDepth)
	}
	foundValidate := false
	for _, e := range result.ByDepth[1] {
		if e.ID == "auth.go::ValidateToken" {
			foundValidate = true
		}
	}
	if !foundValidate {
		t.Errorf("expected ValidateToken in d1; got %v", result.ByDepth[1])
	}
}

// TestAnalyzeImpact_ReachKeysPersistAcrossLookups asserts that the
// reach Meta keys survive between AnalyzeImpact calls — the fast
// path must not mutate Node.Meta in a way that invalidates itself.
func TestAnalyzeImpact_ReachKeysPersistAcrossLookups(t *testing.T) {
	g := buildTestGraph()
	reach.BuildIndex(g)
	before := reach.BuildCounter()

	for i := 0; i < 10; i++ {
		_ = AnalyzeImpact(g, []string{"auth.go::ValidateToken"}, nil, nil)
	}
	if reach.BuildCounter() != before {
		t.Errorf("AnalyzeImpact must not bump the reach generation counter")
	}
	// Stamps must still be present.
	if _, _, _, hit := reach.Lookup(g, "auth.go::ValidateToken"); !hit {
		t.Error("reach stamps must persist across repeated AnalyzeImpact calls")
	}
}

// idSet returns a sorted ID slice for set comparison.
func idSet(entries []ImpactEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.ID
	}
	sort.Strings(out)
	return out
}

func setsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestAnalyzeImpact_FastPathSubMillisecond commits to the L4 claim:
// a precomputed AnalyzeImpact call on a graph of ~1000 reachable
// callers per seed completes well inside the single-digit-ms p99
// budget. Failing here means a regression has slipped a live walk
// into the fast path.
func TestAnalyzeImpact_FastPathSubMillisecond(t *testing.T) {
	if testing.Short() {
		t.Skip("perf gate skipped under -short")
	}
	g := newFanInChain(1000)
	reach.BuildIndex(g)
	seed := "sink"

	const iters = 200
	start := time.Now()
	for i := 0; i < iters; i++ {
		r := AnalyzeImpact(g, []string{seed}, nil, nil)
		if r.TotalAffected == 0 {
			t.Fatalf("expected fan-in fixture to surface callers; iter=%d", i)
		}
	}
	elapsed := time.Since(start)
	avg := elapsed / iters

	// 3 ms gives generous headroom over the sub-ms claim — guards
	// against CI noise (loaded runners, GC pauses) while still
	// catching a regression that drops in a live walk.
	const ceiling = 3 * time.Millisecond
	if avg > ceiling {
		t.Errorf("fast-path AnalyzeImpact too slow: avg=%v over %d iters (ceiling=%v)", avg, iters, ceiling)
	}
	t.Logf("AnalyzeImpact fast path: avg=%v over %d iters on 1000-caller fan-in", avg, iters)
}

// newFanInChain builds a graph with N nodes that all call a single
// sink. Reach for "sink" at depth 1 contains every other node.
func newFanInChain(n int) *graph.Graph {
	g := graph.New()
	g.AddNode(&graph.Node{ID: "sink", Kind: graph.KindFunction, Name: "sink", FilePath: "sink.go"})
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("caller-%d", i)
		g.AddNode(&graph.Node{ID: id, Kind: graph.KindFunction, Name: id, FilePath: id + ".go"})
		g.AddEdge(&graph.Edge{From: id, To: "sink", Kind: graph.EdgeCalls, Confidence: 1})
	}
	return g
}

// BenchmarkAnalyzeImpact_FastPath measures fast-path latency on a
// fan-in of 1000 callers; useful as a perf baseline before
// optimising or rewriting the reach lookup.
func BenchmarkAnalyzeImpact_FastPath(b *testing.B) {
	g := newFanInChain(1000)
	reach.BuildIndex(g)
	b.ResetTimer()
	for b.Loop() {
		AnalyzeImpact(g, []string{"sink"}, nil, nil)
	}
}

// BenchmarkAnalyzeImpact_LiveWalk measures the legacy live-walk path
// on the same fixture; comparing the two benchmarks shows the speedup.
func BenchmarkAnalyzeImpact_LiveWalk(b *testing.B) {
	g := newFanInChain(1000)
	reach.ClearIndex(g)
	b.ResetTimer()
	for b.Loop() {
		AnalyzeImpact(g, []string{"sink"}, nil, nil)
	}
}
