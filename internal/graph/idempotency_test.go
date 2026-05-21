package graph

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAddNode_Idempotent proves the invariant the resilience work added
// to the graph: N duplicate AddNode calls converge to the same Stats()
// and the same secondary-index contents as a single call. Without this,
// a daemon restart that loads a snapshot and then re-runs IndexCtx on
// top of it (which doesn't evict first) produces N× the byFile /
// byName / byRepo slice entries — the B1 symptom.
func TestAddNode_Idempotent(t *testing.T) {
	g := New()
	n := &Node{
		ID:         "repo/a.go::Foo",
		Name:       "Foo",
		Kind:       KindFunction,
		FilePath:   "repo/a.go",
		QualName:   "pkg.Foo",
		RepoPrefix: "repo",
	}

	g.AddNode(n)
	base := g.Stats()
	require.Equal(t, 1, base.TotalNodes)

	for i := 0; i < 10; i++ {
		g.AddNode(n)
	}

	got := g.Stats()
	assert.Equal(t, base.TotalNodes, got.TotalNodes,
		"duplicate AddNode must not grow node count")

	byFile := g.GetFileNodes("repo/a.go")
	assert.Len(t, byFile, 1, "byFile must not duplicate")
	byName := g.FindNodesByName("Foo")
	assert.Len(t, byName, 1, "byName must not duplicate")
	byRepo := g.GetRepoNodes("repo")
	assert.Len(t, byRepo, 1, "byRepo must not duplicate")
	assert.Equal(t, n, g.GetNodeByQualName("pkg.Foo"))
}

// TestAddEdge_Idempotent is the edge counterpart of the node test. With
// the same (From, To, Kind, FilePath, Line), repeated AddEdge calls
// converge to a single adjacency-list entry. This is what made the
// "edges double on every daemon restart" symptom recede.
func TestAddEdge_Idempotent(t *testing.T) {
	g := New()
	g.AddNode(&Node{ID: "a::A", Name: "A", Kind: KindFunction, FilePath: "a"})
	g.AddNode(&Node{ID: "b::B", Name: "B", Kind: KindFunction, FilePath: "b"})

	e := &Edge{From: "b::B", To: "a::A", Kind: EdgeCalls, FilePath: "b", Line: 7}
	for i := 0; i < 10; i++ {
		g.AddEdge(e)
	}

	assert.Equal(t, 1, g.EdgeCount(), "duplicate AddEdge must not grow edge count")
	assert.Len(t, g.GetOutEdges("b::B"), 1, "outEdges must have exactly one entry")
	assert.Len(t, g.GetInEdges("a::A"), 1, "inEdges must have exactly one entry")
}

// TestAddEdge_DifferentFromSameTo guards the edgeKey shape: two edges
// with different From but identical (To, Kind, FilePath, Line) must
// both survive, as distinct entries in the target's inEdges bucket.
// An earlier version of the sidecar omitted From from the key, which
// made two such edges collide at the inEdges[to] index — the second
// AddEdge overwrote the first and downstream BFS traversal lost one
// caller. Cross-repo impact analysis regressed until From landed in
// the key.
func TestAddEdge_DifferentFromSameTo(t *testing.T) {
	g := New()
	g.AddNode(&Node{ID: "target::T", Name: "T", Kind: KindFunction, FilePath: "t"})
	g.AddNode(&Node{ID: "caller1::C1", Name: "C1", Kind: KindFunction, FilePath: "c1"})
	g.AddNode(&Node{ID: "caller2::C2", Name: "C2", Kind: KindFunction, FilePath: "c2"})

	// Both edges lack FilePath/Line — a common shape in tests that
	// construct synthetic graphs. Without From in the key they would
	// dedup to one inEdges entry.
	g.AddEdge(&Edge{From: "caller1::C1", To: "target::T", Kind: EdgeCalls})
	g.AddEdge(&Edge{From: "caller2::C2", To: "target::T", Kind: EdgeCalls})

	in := g.GetInEdges("target::T")
	assert.Len(t, in, 2, "two distinct callers must both appear in inEdges")
}

// TestAddEdge_LineDisambiguates proves that two call-sites from the
// same caller to the same callee at different lines are preserved —
// they're distinct edges, not duplicates. `foo(); foo();` in the same
// function must survive dedup.
func TestAddEdge_LineDisambiguates(t *testing.T) {
	g := New()
	g.AddNode(&Node{ID: "a::A", Name: "A", Kind: KindFunction, FilePath: "a"})
	g.AddNode(&Node{ID: "b::B", Name: "B", Kind: KindFunction, FilePath: "b"})

	g.AddEdge(&Edge{From: "b::B", To: "a::A", Kind: EdgeCalls, FilePath: "b", Line: 7})
	g.AddEdge(&Edge{From: "b::B", To: "a::A", Kind: EdgeCalls, FilePath: "b", Line: 11})

	assert.Equal(t, 2, g.EdgeCount(), "different lines must produce distinct edges")
}

// TestAddNode_Replace verifies that re-adding a node with an updated
// Meta preserves the slice positions and replaces the pointer in place.
// This is the "same ID, new signature / new line" case that happens
// during IncrementalReindex after a file edit.
func TestAddNode_Replace(t *testing.T) {
	g := New()
	n1 := &Node{ID: "a::X", Name: "X", Kind: KindFunction, FilePath: "a",
		Meta: map[string]any{"signature": "X()"}}
	g.AddNode(n1)

	n2 := &Node{ID: "a::X", Name: "X", Kind: KindFunction, FilePath: "a",
		Meta: map[string]any{"signature": "X(arg int)"}}
	g.AddNode(n2)

	got := g.GetNode("a::X")
	require.NotNil(t, got)
	assert.Equal(t, "X(arg int)", got.Meta["signature"],
		"replacement must install new pointer")
	assert.Len(t, g.GetFileNodes("a"), 1, "byFile must not grow on replace")
	assert.Len(t, g.FindNodesByName("X"), 1, "byName must not grow on replace")
	// The slice entry must be the new pointer — readers iterate byFile
	// and rely on it reflecting the current node state.
	assert.Same(t, n2, g.GetFileNodes("a")[0])
}

// TestAddNode_MigrateBuckets verifies that when a replacement changes
// the node's FilePath / Name / RepoPrefix, the secondary-index entry
// moves from the old bucket to the new one. Without this, a rename
// (unusual but legal) would leave ghost entries in both buckets.
func TestAddNode_MigrateBuckets(t *testing.T) {
	g := New()
	g.AddNode(&Node{ID: "x::X", Name: "OldName", Kind: KindFunction,
		FilePath: "old.go", RepoPrefix: "oldrepo", QualName: "pkg.Old"})
	g.AddNode(&Node{ID: "x::X", Name: "NewName", Kind: KindFunction,
		FilePath: "new.go", RepoPrefix: "newrepo", QualName: "pkg.New"})

	assert.Empty(t, g.GetFileNodes("old.go"), "old bucket must be emptied")
	assert.Len(t, g.GetFileNodes("new.go"), 1, "new bucket must have the entry")
	assert.Empty(t, g.FindNodesByName("OldName"))
	assert.Len(t, g.FindNodesByName("NewName"), 1)
	assert.Empty(t, g.GetRepoNodes("oldrepo"))
	assert.Len(t, g.GetRepoNodes("newrepo"), 1)
	assert.Nil(t, g.GetNodeByQualName("pkg.Old"))
	assert.NotNil(t, g.GetNodeByQualName("pkg.New"))
}

// TestAddNode_PreservesRepoPrefixOnEmptyDowngrade pins the warmup bug
// where some path re-AddNode'd existing repo-stamped nodes with
// RepoPrefix="" — clearing them out of byRepo[prefix] without touching
// the underlying nodes map. The user-visible symptom: per-repo queries
// (RepoStats / GetRepoNodes / RepoMemoryEstimate) returned empty for
// repos whose nodes were still present in the graph. Defensive fix:
// a non-empty prev RepoPrefix is sticky — the empty new value is
// promoted to prev's value rather than allowed to silently strip the
// node from its bucket.
func TestAddNode_PreservesRepoPrefixOnEmptyDowngrade(t *testing.T) {
	g := New()
	original := &Node{
		ID: "myrepo/file.go::Foo", Name: "Foo", Kind: KindFunction,
		FilePath: "myrepo/file.go", RepoPrefix: "myrepo",
	}
	g.AddNode(original)
	require.Len(t, g.GetRepoNodes("myrepo"), 1, "node must land in byRepo at first add")

	// Re-add with empty RepoPrefix (the buggy caller).
	g.AddNode(&Node{
		ID: "myrepo/file.go::Foo", Name: "Foo", Kind: KindFunction,
		FilePath: "myrepo/file.go",
		// RepoPrefix intentionally empty.
	})

	assert.Len(t, g.GetRepoNodes("myrepo"), 1,
		"byRepo[myrepo] must still contain the node after empty-prefix re-add")
	assert.NotNil(t, g.GetNode("myrepo/file.go::Foo"),
		"node itself must still exist")
	assert.Equal(t, "myrepo", g.GetNode("myrepo/file.go::Foo").RepoPrefix,
		"RepoPrefix on the stored node must be preserved")
}

// TestEvictFile_SwapWithLast exercises the sidecar-based swap-with-last
// removal path. Uses enough nodes per file that iteration order would
// surface a mis-tracked sidecar position. The assertion is simple: post
// eviction, the graph is empty of entries for that file.
func TestEvictFile_SwapWithLast(t *testing.T) {
	g := New()
	for i := 0; i < 100; i++ {
		g.AddNode(&Node{
			ID:       fmt.Sprintf("f.go::Sym%d", i),
			Name:     fmt.Sprintf("Sym%d", i),
			Kind:     KindFunction,
			FilePath: "f.go",
		})
	}
	assert.Len(t, g.GetFileNodes("f.go"), 100)

	n, _ := g.EvictFile("f.go")
	assert.Equal(t, 100, n)
	assert.Empty(t, g.GetFileNodes("f.go"))
	assert.Equal(t, 0, g.NodeCount())
}

// TestRestartStability simulates the daemon-restart cycle: snapshot
// into a fresh graph (via AddNode/AddEdge replay, which is what
// loadSnapshot does), and verify Stats() matches the original. Repeat
// many times to catch any state that drifts across restarts.
//
// Before the sidecar landed, Stats().TotalEdges doubled on every cycle;
// after, the invariant holds for arbitrary N.
func TestRestartStability(t *testing.T) {
	orig := buildRepresentativeGraph()
	want := orig.Stats()

	for cycle := 0; cycle < 5; cycle++ {
		replay := New()
		for _, n := range orig.AllNodes() {
			replay.AddNode(n)
		}
		for _, e := range orig.AllEdges() {
			replay.AddEdge(e)
		}

		// Simulate a second "IndexCtx on top" pass — this is what
		// the old warmup did after loadSnapshot. Without idempotent
		// writes, this pass doubles every edge.
		for _, n := range orig.AllNodes() {
			replay.AddNode(n)
		}
		for _, e := range orig.AllEdges() {
			replay.AddEdge(e)
		}

		got := replay.Stats()
		assert.Equal(t, want.TotalNodes, got.TotalNodes,
			"cycle %d: node count drifted", cycle)
		assert.Equal(t, want.TotalEdges, got.TotalEdges,
			"cycle %d: edge count drifted (B1 regression)", cycle)
	}
}

func buildRepresentativeGraph() *Graph {
	g := New()
	// Build a small call graph that stresses every secondary index:
	// multiple files, multiple names, multiple repos, calls + imports.
	files := []struct {
		path, repo string
	}{
		{"r1/a.go", "r1"},
		{"r1/b.go", "r1"},
		{"r2/c.go", "r2"},
	}
	for _, f := range files {
		for i := 0; i < 5; i++ {
			g.AddNode(&Node{
				ID:         fmt.Sprintf("%s::Fn%d", f.path, i),
				Name:       fmt.Sprintf("Fn%d", i),
				Kind:       KindFunction,
				FilePath:   f.path,
				RepoPrefix: f.repo,
			})
		}
	}
	// Add a few call edges between files.
	g.AddEdge(&Edge{From: "r1/a.go::Fn0", To: "r1/b.go::Fn1", Kind: EdgeCalls, FilePath: "r1/a.go", Line: 10})
	g.AddEdge(&Edge{From: "r1/a.go::Fn0", To: "r2/c.go::Fn2", Kind: EdgeCalls, FilePath: "r1/a.go", Line: 12})
	g.AddEdge(&Edge{From: "r1/b.go::Fn3", To: "r2/c.go::Fn4", Kind: EdgeCalls, FilePath: "r1/b.go", Line: 5})
	return g
}

// TestReindexEdge_UpdatesSidecar verifies ReindexEdge migrates the
// inEdges bucket + both sidecars when the resolver changes an edge's
// To field (unresolved::X → real::X). A bug here would show up as
// GetInEdges returning zero entries after resolve, or later AddEdge
// refusing to dedup because the key changed out from under the sidecar.
func TestReindexEdge_UpdatesSidecar(t *testing.T) {
	g := New()
	g.AddNode(&Node{ID: "a::A", Name: "A", Kind: KindFunction, FilePath: "a"})
	g.AddNode(&Node{ID: "b::B", Name: "B", Kind: KindFunction, FilePath: "b"})
	g.AddNode(&Node{ID: "unresolved::real", Name: "real", Kind: KindFunction, FilePath: "u"})

	e := &Edge{From: "a::A", To: "unresolved::real", Kind: EdgeCalls, FilePath: "a", Line: 3}
	g.AddEdge(e)

	require.Len(t, g.GetInEdges("unresolved::real"), 1)
	require.Len(t, g.GetInEdges("b::B"), 0)

	// Resolver-style mutation.
	oldTo := e.To
	e.To = "b::B"
	g.ReindexEdge(e, oldTo)

	assert.Len(t, g.GetInEdges("unresolved::real"), 0,
		"old target bucket must be emptied")
	assert.Len(t, g.GetInEdges("b::B"), 1,
		"new target bucket must hold the edge")

	// Adding the same edge with its NEW identity must dedup via the
	// updated sidecar — if ReindexEdge forgot to rewrite the
	// outEdgeIdx key, this would append a duplicate.
	g.AddEdge(e)
	assert.Equal(t, 1, g.EdgeCount(), "AddEdge after ReindexEdge must still dedup")
}

// TestRemoveEdgeFromBucket_SwappedEdgeWithMutatedTo regresses a daemon
// crash:
//
//	panic: runtime error: index out of range [N] with length N
//	  graph.addEdgeToBucket
//	  graph.(*Graph).ReindexEdge
//	  resolver.(*Resolver).ResolveAll
//
// The resolver's serial pass mutates `j.edge.To = j.newTo` BEFORE
// taking the shard lock. If the swap-with-last in
// removeEdgeFromBucket lands on an edge whose .To was mutated in the
// same flight (e.g. another job in the same bucket), recomputing
// keyOf(swapped) returns the NEW key while the sidecar still has an
// entry under the ORIGINAL key pointing past the shrunk slice. The
// next AddEdge that collides with the orphaned key panics.
//
// The fix stores each entry's insertion-time edgeKey in a parallel
// slice (outEdgeKeys / inEdgeKeys) so the sidecar update is
// independent of the live Edge struct.
func TestRemoveEdgeFromBucket_SwappedEdgeWithMutatedTo(t *testing.T) {
	g := New()
	g.AddNode(&Node{ID: "a::A", Name: "A", Kind: KindFunction, FilePath: "a"})
	g.AddNode(&Node{ID: "b::B", Name: "B", Kind: KindFunction, FilePath: "b"})
	g.AddNode(&Node{ID: "x::X", Name: "X", Kind: KindFunction, FilePath: "x"})
	g.AddNode(&Node{ID: "y::Y", Name: "Y", Kind: KindFunction, FilePath: "y"})

	// Two edges share an unresolved bucket. We'll mutate eSwapped's To
	// out-of-band (mimicking the resolver's pre-lock mutation) before
	// removing eHead, forcing eSwapped to be the swap-with-last
	// element. With the bug, the sidecar update used keyOf(eSwapped)
	// — a different key than the one eSwapped was indexed under —
	// leaving a stale entry that pointed past the shrunk slice.
	const target = "unresolved::shared"
	eHead := &Edge{From: "a::A", To: target, Kind: EdgeCalls, FilePath: "a", Line: 1}
	eSwapped := &Edge{From: "b::B", To: target, Kind: EdgeCalls, FilePath: "b", Line: 2}
	g.AddEdge(eHead)
	g.AddEdge(eSwapped)
	require.Len(t, g.GetInEdges(target), 2)

	// Out-of-band mutation: eSwapped.To changes BUT we don't yet
	// ReindexEdge. This models the in-flight window in
	// resolver.go's serial pass.
	eSwapped.To = "x::X"

	// Now remove eHead via ReindexEdge — this triggers the swap that
	// previously corrupted the sidecar.
	oldHead := target
	eHead.To = "y::Y"
	g.ReindexEdge(eHead, oldHead)

	// With the bug, inEdgeIdx[target] still held an orphan entry under
	// eSwapped's ORIGINAL key (To=target) at position 1 — past the
	// now-shrunk slice (length 1, valid index only 0). Any subsequent
	// AddEdge whose key collides with that stale entry would do
	// `bucket[target][1] = newEdge` and panic with
	// "index out of range [1] with length 1".
	//
	// Construct exactly that collision: a fresh edge sharing
	// eSwapped's original (From, To, Kind, FilePath, Line) tuple,
	// which is what the resolver does when it pre-stages a duplicate
	// pending edge from another file at the same line.
	collision := &Edge{From: "b::B", To: target, Kind: EdgeCalls, FilePath: "b", Line: 2}
	require.NotPanics(t, func() {
		g.AddEdge(collision)
	}, "addEdgeToBucket must not panic on a stale sidecar position")

	// eHead has been migrated to its new target.
	assert.Len(t, g.GetInEdges("y::Y"), 1, "eHead's new target should hold one edge")
}

// TestReindexEdge_OutEdgeKeysStayConsistent regresses the daemon
// warmup panic:
//
//	panic: runtime error: index out of range [61] with length 58
//	  graph.removeEdgeFromBucket
//	  graph.(*Graph).evictEdgesLocked
//	  graph.(*Graph).EvictFile
//	  indexer.(*Indexer).indexFile
//	  indexer.(*Indexer).IncrementalReindex
//
// The failure mode: ReindexEdge updates outEdgeIdx[oldKey→newKey] but
// previously did NOT update the parallel outEdgeKeys[pos] slice. A
// later swap-with-last removal in the same outEdges bucket reads
// outEdgeKeys[swappedPos] — finds the stale insertion-time key — and
// re-inserts THAT key into outEdgeIdx pointing at the swapped slot.
// outEdgeIdx then holds both the live newKey (still pointing at the
// original pre-swap position) AND a stale-key entry. The next op
// that walks back to the original pos finds the slice has shrunk
// past it and panics.
//
// The fix: ReindexEdge rewrites outEdgeKeys[pos] = newKey alongside
// the outEdgeIdx update so the parallel slice never holds stale keys.
func TestReindexEdge_OutEdgeKeysStayConsistent(t *testing.T) {
	g := New()
	g.AddNode(&Node{ID: "a::A", Name: "A", Kind: KindFunction, FilePath: "a"})
	g.AddNode(&Node{ID: "t1", Name: "t1", Kind: KindFunction, FilePath: "t1"})
	g.AddNode(&Node{ID: "t2", Name: "t2", Kind: KindFunction, FilePath: "t2"})
	g.AddNode(&Node{ID: "t3", Name: "t3", Kind: KindFunction, FilePath: "t3"})
	g.AddNode(&Node{ID: "t2-prime", Name: "t2'", Kind: KindFunction, FilePath: "t2p"})
	g.AddNode(&Node{ID: "t2-prime-prime", Name: "t2''", Kind: KindFunction, FilePath: "t2pp"})

	// Three edges share the same From, populating one outEdges bucket
	// with three slots. Distinct lines so the keys differ.
	e1 := &Edge{From: "a::A", To: "t1", Kind: EdgeCalls, FilePath: "a", Line: 1}
	e2 := &Edge{From: "a::A", To: "t2", Kind: EdgeCalls, FilePath: "a", Line: 2}
	e3 := &Edge{From: "a::A", To: "t3", Kind: EdgeCalls, FilePath: "a", Line: 3}
	g.AddEdge(e1)
	g.AddEdge(e2)
	g.AddEdge(e3)
	require.Len(t, g.GetOutEdges("a::A"), 3)

	// ReindexEdge e2 — outEdgeKeys[1] would stay stale before the fix.
	oldTo := e2.To
	e2.To = "t2-prime"
	g.ReindexEdge(e2, oldTo)

	// Force a swap-with-last in the outEdges["a::A"] bucket by
	// removing e1. With the bug, this propagates the stale key for
	// slot 1 (e2's original key) into outEdgeIdx.
	require.True(t, g.RemoveEdge(e1.From, e1.To, e1.Kind))

	// ReindexEdge e2 a second time — drives outEdgeIdx into the
	// inconsistent state where it holds both the new key and the
	// stale key from the previous swap.
	oldTo = e2.To
	e2.To = "t2-prime-prime"
	g.ReindexEdge(e2, oldTo)

	// Removal that touches the bucket must NOT panic. With the bug,
	// removing e3 via its resolved key triggered
	// `slice[pos] = slice[last]` with pos past the shrunk slice.
	require.NotPanics(t, func() {
		g.RemoveEdge(e3.From, e3.To, e3.Kind)
	}, "swap-with-last after repeated ReindexEdge must not panic")

	// e2 still queryable at its final target — sanity check that the
	// bucket bookkeeping survived intact.
	out := g.GetOutEdges("a::A")
	require.Len(t, out, 1)
	assert.Equal(t, "t2-prime-prime", out[0].To)
}

// TestEvictFile_AfterReindex regresses the same panic via the actual
// eviction path the daemon hit (EvictFile → evictEdgesLocked) instead
// of going through the public RemoveEdge API. The fixture stages the
// exact corruption window the daemon panic describes:
//
//  1. A multi-edge outEdges bucket on a single From.
//  2. ReindexEdge against a non-last slot in that bucket — outEdgeKeys
//     for that slot becomes stale (still holds the pre-mutation key).
//  3. A swap-with-last removal earlier in the bucket pulls the stale
//     key into outEdgeIdx pointing at the swapped position.
//  4. The slice subsequently shrinks past that position.
//  5. EvictFile on the reindexed edge's NEW target then walks
//     inEdges[that target], grabs the still-correct live key from
//     inEdgeKeys, and calls removeEdgeFromBucket(outEdges, ...) on
//     the From bucket. With the bug, outEdgeIdx still has the live
//     key pointing past the now-shrunk slice → panic.
func TestEvictFile_AfterReindex(t *testing.T) {
	g := New()
	g.AddNode(&Node{ID: "src/a.go::A", Name: "A", Kind: KindFunction, FilePath: "src/a.go"})
	g.AddNode(&Node{ID: "t1.go::T1", Name: "T1", Kind: KindFunction, FilePath: "t1.go"})
	g.AddNode(&Node{ID: "t2.go::T2", Name: "T2", Kind: KindFunction, FilePath: "t2.go"})
	g.AddNode(&Node{ID: "t3.go::T3", Name: "T3", Kind: KindFunction, FilePath: "t3.go"})
	g.AddNode(&Node{ID: "t2p.go::T2P", Name: "T2P", Kind: KindFunction, FilePath: "t2p.go"})

	// Three outgoing edges from A — slot 1 is the one we'll reindex.
	e1 := &Edge{From: "src/a.go::A", To: "t1.go::T1", Kind: EdgeCalls, FilePath: "src/a.go", Line: 1}
	e2 := &Edge{From: "src/a.go::A", To: "t2.go::T2", Kind: EdgeCalls, FilePath: "src/a.go", Line: 2}
	e3 := &Edge{From: "src/a.go::A", To: "t3.go::T3", Kind: EdgeCalls, FilePath: "src/a.go", Line: 3}
	g.AddEdge(e1)
	g.AddEdge(e2)
	g.AddEdge(e3)

	// Step 1: reindex e2's To. Without the fix, outEdgeKeys[1] keeps
	// the pre-mutation key while outEdgeIdx swaps to the new key.
	old := e2.To
	e2.To = "t2p.go::T2P"
	g.ReindexEdge(e2, old)

	// Step 2: evict T1 — its inEdges bucket holds e1; Phase 2 of
	// evictEdgesLocked calls removeEdgeFromBucket(outEdges["src/a.go::A"], k_for_e1).
	// Inside, swap-with-last picks slot 2's key (k_for_e3 — correct)
	// because slot 2 is what the swap consumes. So no panic yet, but
	// after the swap the bucket is shape [e3, e2] with outEdgeKeys
	// = [k_for_e3, STALE_pre-reindex_e2_key].
	require.NotPanics(t, func() { g.EvictFile("t1.go") })

	// Step 3: evict T3 — its inEdges bucket now points at the
	// swapped slot 0 (e3). removeEdgeFromBucket(outEdges, k_for_e3)
	// runs, swap-with-last picks up outEdgeKeys[1] which is the
	// STALE key. With the bug, that stale key gets re-inserted into
	// outEdgeIdx at position 0 alongside the still-live e2 key
	// (which now points at position 1, but the slice has shrunk to
	// length 1).
	require.NotPanics(t, func() { g.EvictFile("t3.go") })

	// Step 4: evict T2P. inEdges[T2P] holds e2 with inEdgeKeys
	// carrying the LIVE key (insertion via addEdgeToBucket during
	// ReindexEdge used the new key). removeEdgeFromBucket(outEdges
	// ["src/a.go::A"], LIVE_key) looks up outEdgeIdx[LIVE_key] = 1,
	// then tries slice[1] in a slice of length 1 → panic with the
	// bug, clean removal with the fix.
	require.NotPanics(t, func() {
		g.EvictFile("t2p.go")
	}, "EvictFile on the reindexed edge's new target must not panic on stale outEdgeIdx")

	// All edges removed — bucket should be empty.
	assert.Empty(t, g.GetOutEdges("src/a.go::A"), "outEdges bucket must drain after every target was evicted")
}

// edgeIdentityGraph builds a two-node graph with one A→B calls edge at
// the given Origin, returning the graph and the live in-graph edge.
func edgeIdentityGraph(t *testing.T, origin string) (*Graph, *Edge) {
	t.Helper()
	g := New()
	g.AddNode(&Node{ID: "p/a.go::A", Name: "A", Kind: KindFunction, FilePath: "p/a.go"})
	g.AddNode(&Node{ID: "p/b.go::B", Name: "B", Kind: KindFunction, FilePath: "p/b.go"})
	g.AddEdge(&Edge{From: "p/a.go::A", To: "p/b.go::B", Kind: EdgeCalls, FilePath: "p/a.go", Line: 7, Origin: origin})
	out := g.GetOutEdges("p/a.go::A")
	require.Len(t, out, 1)
	return g, out[0]
}

// TestSetEdgeProvenance_ChangesIdentityAndCounts proves SetEdgeProvenance
// is a delete-then-insert of the edge's identity: a real Origin change
// flips the IdentityHash and bumps the revision counter by exactly one,
// while the logical adjacency-list slot is untouched.
func TestSetEdgeProvenance_ChangesIdentityAndCounts(t *testing.T) {
	g, e := edgeIdentityGraph(t, OriginTextMatched)
	require.Equal(t, 0, g.EdgeIdentityRevisions(), "fresh graph has no provenance churn")

	before := e.IdentityHash()
	changed := g.SetEdgeProvenance(e, OriginLSPResolved)

	assert.True(t, changed, "upgrading Origin must report an identity change")
	assert.Equal(t, OriginLSPResolved, e.Origin, "Origin must be applied")
	assert.NotEqual(t, before, e.IdentityHash(), "identity hash must change with Origin")
	assert.Equal(t, 1, g.EdgeIdentityRevisions(), "exactly one revision recorded")

	// The logical edge is unchanged — same single adjacency entry.
	assert.Len(t, g.GetOutEdges("p/a.go::A"), 1, "outEdges count must not change")
	assert.Len(t, g.GetInEdges("p/b.go::B"), 1, "inEdges count must not change")
}

// TestSetEdgeProvenance_NoOpWhenOriginUnchanged proves a SetEdgeProvenance
// call that does not actually change Origin is a no-op: identity stable,
// counter untouched, return value false.
func TestSetEdgeProvenance_NoOpWhenOriginUnchanged(t *testing.T) {
	g, e := edgeIdentityGraph(t, OriginASTResolved)
	before := e.IdentityHash()

	changed := g.SetEdgeProvenance(e, OriginASTResolved)

	assert.False(t, changed, "setting Origin to its current value is a no-op")
	assert.Equal(t, before, e.IdentityHash(), "identity hash must be stable on a no-op")
	assert.Equal(t, 0, g.EdgeIdentityRevisions(), "a no-op must not bump the counter")
}

// TestSetEdgeProvenance_RederivesTierWhenSet confirms Tier — the sole
// Origin-derived label on an edge — is recomputed when it was already
// populated, and left empty (the in-memory default) when it was not.
func TestSetEdgeProvenance_RederivesTierWhenSet(t *testing.T) {
	// Tier already set: must be re-derived from the new Origin.
	g, e := edgeIdentityGraph(t, OriginTextMatched)
	e.Tier = ResolvedBy(OriginTextMatched)
	g.SetEdgeProvenance(e, OriginLSPResolved)
	assert.Equal(t, ResolvedBy(OriginLSPResolved), e.Tier, "populated Tier must track the new Origin")

	// Tier left empty: must stay empty rather than start being stamped.
	g2, e2 := edgeIdentityGraph(t, OriginTextMatched)
	g2.SetEdgeProvenance(e2, OriginLSPResolved)
	assert.Equal(t, "", e2.Tier, "an unset Tier must remain unset")
}

// TestAddEdge_ReaddWithUpgradedOriginCounts proves the second mutation
// path: re-adding an edge with the same logical key but an upgraded
// Origin (the resolver's AddEdge-based upgrade path) replaces the slot
// in place AND is counted as an identity revision — without creating a
// duplicate parallel edge.
func TestAddEdge_ReaddWithUpgradedOriginCounts(t *testing.T) {
	g, _ := edgeIdentityGraph(t, OriginTextMatched)
	require.Equal(t, 0, g.EdgeIdentityRevisions())

	// Re-add the same logical edge with a stronger Origin.
	g.AddEdge(&Edge{From: "p/a.go::A", To: "p/b.go::B", Kind: EdgeCalls, FilePath: "p/a.go", Line: 7, Origin: OriginLSPResolved})

	assert.Equal(t, 1, g.EdgeCount(), "re-add must not create a parallel edge")
	assert.Len(t, g.GetOutEdges("p/a.go::A"), 1, "still one outEdge")
	assert.Len(t, g.GetInEdges("p/b.go::B"), 1, "still one inEdge")
	assert.Equal(t, 1, g.EdgeIdentityRevisions(), "the Origin upgrade on re-add must be counted once")
	assert.Equal(t, OriginLSPResolved, g.GetOutEdges("p/a.go::A")[0].Origin, "newer Origin wins")
}

// TestAddEdge_ReaddWithSameOriginDoesNotCount proves an idempotent
// re-add carrying the SAME Origin is not mistaken for provenance churn.
func TestAddEdge_ReaddWithSameOriginDoesNotCount(t *testing.T) {
	g, _ := edgeIdentityGraph(t, OriginASTResolved)
	for i := 0; i < 5; i++ {
		g.AddEdge(&Edge{From: "p/a.go::A", To: "p/b.go::B", Kind: EdgeCalls, FilePath: "p/a.go", Line: 7, Origin: OriginASTResolved})
	}
	assert.Equal(t, 1, g.EdgeCount(), "idempotent re-add must not grow the edge count")
	assert.Equal(t, 0, g.EdgeIdentityRevisions(), "re-add with an unchanged Origin is not a revision")
}

// TestVerifyEdgeIdentities_PassesOnNormalGraph proves a graph built
// only through the sanctioned mutation paths (AddEdge, SetEdgeProvenance)
// is internally consistent — the out-edge and in-edge views agree on
// every edge's provenance-bearing identity.
func TestVerifyEdgeIdentities_PassesOnNormalGraph(t *testing.T) {
	g := New()
	for _, id := range []string{"p/a.go::A", "p/b.go::B", "p/c.go::C"} {
		g.AddNode(&Node{ID: id, Name: id, Kind: KindFunction, FilePath: id})
	}
	g.AddEdge(&Edge{From: "p/a.go::A", To: "p/b.go::B", Kind: EdgeCalls, FilePath: "p/a.go", Line: 3, Origin: OriginTextMatched})
	g.AddEdge(&Edge{From: "p/a.go::A", To: "p/c.go::C", Kind: EdgeCalls, FilePath: "p/a.go", Line: 4, Origin: OriginASTResolved})
	g.AddEdge(&Edge{From: "p/b.go::B", To: "p/c.go::C", Kind: EdgeReferences, FilePath: "p/b.go", Line: 9})

	require.NoError(t, g.VerifyEdgeIdentities(), "freshly built graph must be identity-consistent")

	// A sanctioned provenance change keeps the graph consistent.
	out := g.GetOutEdges("p/a.go::A")
	require.NotEmpty(t, out)
	g.SetEdgeProvenance(out[0], OriginLSPResolved)
	require.NoError(t, g.VerifyEdgeIdentities(), "SetEdgeProvenance must preserve identity consistency")
}

// TestVerifyEdgeIdentities_CatchesDivergentOrigin proves the verifier
// is not vacuous: when an edge's Origin is changed on only one
// adjacency view (the failure mode of mutating a copied edge instead
// of routing through SetEdgeProvenance), VerifyEdgeIdentities reports
// the inconsistency.
func TestVerifyEdgeIdentities_CatchesDivergentOrigin(t *testing.T) {
	g := New()
	g.AddNode(&Node{ID: "p/a.go::A", Name: "A", Kind: KindFunction, FilePath: "p/a.go"})
	g.AddNode(&Node{ID: "p/b.go::B", Name: "B", Kind: KindFunction, FilePath: "p/b.go"})
	g.AddEdge(&Edge{From: "p/a.go::A", To: "p/b.go::B", Kind: EdgeCalls, FilePath: "p/a.go", Line: 7, Origin: OriginTextMatched})
	require.NoError(t, g.VerifyEdgeIdentities())

	// Simulate the bug: the in-edge bucket gets a *different* edge
	// object whose Origin diverges from the out-edge view. addEdgeToBucket
	// keys on the Origin-free logical key, so this overwrites the slot
	// with a copy rather than appending.
	sTo := g.shardFor("p/b.go::B")
	sTo.mu.Lock()
	divergent := &Edge{From: "p/a.go::A", To: "p/b.go::B", Kind: EdgeCalls, FilePath: "p/a.go", Line: 7, Origin: OriginLSPResolved}
	addEdgeToBucket(sTo.inEdges, sTo.inEdgeKeys, sTo.inEdgeIdx, "p/b.go::B", divergent)
	sTo.mu.Unlock()

	err := g.VerifyEdgeIdentities()
	require.Error(t, err, "a divergent-Origin edge across adjacency views must be caught")
	assert.Contains(t, err.Error(), "p/a.go::A", "the error must name the offending edge")
}
