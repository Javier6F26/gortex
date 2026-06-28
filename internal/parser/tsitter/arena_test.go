package tsitter

import "testing"

// TestNodeArenaStablePointers guards the arena's load-bearing invariant:
// a pointer returned by alloc stays valid after later allocations grow the
// arena onto a new backing chunk. If alloc ever appended into a single
// reallocating slice, earlier &slice[i] pointers would dangle and node data
// would silently corrupt mid-walk.
func TestNodeArenaStablePointers(t *testing.T) {
	a := newNodeArena()
	const n = arenaMaxChunk*2 + 17 // cross several chunk boundaries incl. the max-size cap
	ptrs := make([]*Node, n)
	for i := range ptrs {
		p := a.alloc()
		if p == nil {
			t.Fatalf("alloc %d returned nil", i)
		}
		p.valid = true
		ptrs[i] = p
	}
	seen := make(map[*Node]bool, n)
	for i, p := range ptrs {
		if seen[p] {
			t.Fatalf("alloc %d aliased an earlier node pointer", i)
		}
		seen[p] = true
		if !p.valid {
			t.Fatalf("node %d was clobbered by a later chunk allocation", i)
		}
	}
}

// TestNodeArenaGeometricGrowth confirms the first chunk is small (low waste
// for tiny files) and chunks grow up to the cap (few objects for deep files).
func TestNodeArenaGeometricGrowth(t *testing.T) {
	a := newNodeArena()
	a.alloc()
	if got := len(a.chunks[0]); got != arenaFirstChunk {
		t.Fatalf("first chunk size = %d, want %d", got, arenaFirstChunk)
	}
	// Drain well past the cap; no chunk may exceed the max.
	for i := 0; i < arenaMaxChunk*3; i++ {
		a.alloc()
		if got := len(a.chunks[a.ci]); got > arenaMaxChunk {
			t.Fatalf("chunk grew to %d, exceeds cap %d", got, arenaMaxChunk)
		}
	}
}

// TestNodeArenaResetReusesChunks is the pooling invariant: reset rewinds the
// cursor but KEEPS the backing chunks, so refilling to the same size
// allocates no new chunk, and any stale Node from the previous round is
// cleared — a recycled arena must never pin a closed tree.
func TestNodeArenaResetReusesChunks(t *testing.T) {
	a := newNodeArena()
	const n = arenaMaxChunk + 100 // span more than one chunk
	for i := 0; i < n; i++ {
		a.alloc().valid = true
	}
	want := len(a.chunks)
	if want < 2 {
		t.Fatalf("test needs a multi-chunk arena, got %d chunk(s)", want)
	}

	a.reset()
	if len(a.chunks) != want {
		t.Fatalf("reset dropped chunks: have %d, want %d (chunks must be retained)", len(a.chunks), want)
	}
	if a.chunks[0][0].valid {
		t.Fatal("reset left a stale Node valid — a recycled arena would pin its tree")
	}

	for i := 0; i < n; i++ {
		a.alloc().valid = true
	}
	if len(a.chunks) != want {
		t.Fatalf("refill allocated fresh chunks: have %d, want %d (chunks not reused)", len(a.chunks), want)
	}
}
