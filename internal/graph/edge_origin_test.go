package graph

import "testing"

func TestOriginRank_Ordering(t *testing.T) {
	// The ordering expresses the design contract: LSP-verified evidence
	// outranks AST-extracted evidence, which outranks name-only matches.
	order := []string{
		OriginLSPResolved,
		OriginLSPDispatch,
		OriginASTResolved,
		OriginASTInferred,
		OriginTextMatched,
	}
	for i := 0; i < len(order)-1; i++ {
		if OriginRank(order[i]) <= OriginRank(order[i+1]) {
			t.Errorf("expected rank(%s)=%d > rank(%s)=%d",
				order[i], OriginRank(order[i]),
				order[i+1], OriginRank(order[i+1]))
		}
	}
}

func TestOriginRank_UnknownReturnsZero(t *testing.T) {
	if got := OriginRank(""); got != 0 {
		t.Errorf("empty origin: got rank %d, want 0", got)
	}
	if got := OriginRank("bogus_value"); got != 0 {
		t.Errorf("unknown origin: got rank %d, want 0", got)
	}
}

func TestMeetsMinTier(t *testing.T) {
	tests := []struct {
		origin, minTier string
		want            bool
	}{
		{OriginLSPResolved, OriginLSPResolved, true},
		{OriginLSPResolved, OriginTextMatched, true},
		{OriginASTResolved, OriginLSPResolved, false},
		{OriginTextMatched, OriginASTInferred, false},
		{OriginLSPDispatch, OriginASTResolved, true},
		{"", OriginLSPResolved, false},
		{OriginLSPResolved, "", true}, // no filter = all pass
		{"", "", true},                // no filter, no origin = pass
	}
	for _, tt := range tests {
		got := MeetsMinTier(tt.origin, tt.minTier)
		if got != tt.want {
			t.Errorf("MeetsMinTier(%q, %q) = %v, want %v",
				tt.origin, tt.minTier, got, tt.want)
		}
	}
}

func TestDefaultOriginFor_SemanticSource(t *testing.T) {
	// Any non-empty semantic source means a compiler-grade provider
	// confirmed the edge — should map to LSP-resolved.
	got := DefaultOriginFor(EdgeCalls, 1.0, "go-types")
	if got != OriginLSPResolved {
		t.Errorf("got %q, want %q", got, OriginLSPResolved)
	}
}

func TestDefaultOriginFor_ImplementsWithSource(t *testing.T) {
	// Interface → implementation via semantic provider = LSP dispatch.
	got := DefaultOriginFor(EdgeImplements, 1.0, "lsp")
	if got != OriginLSPDispatch {
		t.Errorf("got %q, want %q", got, OriginLSPDispatch)
	}
}

func TestDefaultOriginFor_StructuralAST(t *testing.T) {
	for _, kind := range []EdgeKind{
		EdgeDefines, EdgeImports, EdgeExtends, EdgeMemberOf,
		EdgeImplements, EdgeProvides, EdgeConsumes,
	} {
		got := DefaultOriginFor(kind, 0, "")
		if got != OriginASTResolved {
			t.Errorf("kind=%s: got %q, want %q", kind, got, OriginASTResolved)
		}
	}
}

func TestDefaultOriginFor_ConfidenceBuckets(t *testing.T) {
	tests := []struct {
		conf float64
		want string
	}{
		{1.0, OriginASTResolved},
		{0.95, OriginASTResolved},
		{0.7, OriginASTInferred},
		{0.5, OriginASTInferred},
		{0.3, OriginTextMatched},
		{0, OriginTextMatched},
	}
	for _, tt := range tests {
		got := DefaultOriginFor(EdgeCalls, tt.conf, "")
		if got != tt.want {
			t.Errorf("confidence=%v: got %q, want %q", tt.conf, got, tt.want)
		}
	}
}

// TestEdgeIdentityHash_StableForFixedOrigin proves IdentityHash is a
// pure function of (From, To, Kind, FilePath, Line, Origin): two
// separately-constructed edges with identical fields hash equal.
func TestEdgeIdentityHash_StableForFixedOrigin(t *testing.T) {
	a := &Edge{From: "p/a.go::A", To: "p/b.go::B", Kind: EdgeCalls, FilePath: "p/a.go", Line: 12, Origin: OriginASTResolved}
	b := &Edge{From: "p/a.go::A", To: "p/b.go::B", Kind: EdgeCalls, FilePath: "p/a.go", Line: 12, Origin: OriginASTResolved}

	if a.IdentityHash() != b.IdentityHash() {
		t.Fatal("edges with identical fields must share an identity hash")
	}
}

// TestEdgeIdentityHash_DiffersOnlyByOrigin proves the deliverable: the
// identity hash changes iff Origin changes when every other field is
// held fixed. Each distinct tier yields a distinct identity, and the
// logical key alone (Origin-free) is NOT enough to pin the identity.
func TestEdgeIdentityHash_DiffersOnlyByOrigin(t *testing.T) {
	base := func(origin string) *Edge {
		return &Edge{From: "p/a.go::A", To: "p/b.go::B", Kind: EdgeCalls, FilePath: "p/a.go", Line: 12, Origin: origin}
	}
	origins := []string{
		OriginLSPResolved, OriginLSPDispatch, OriginASTResolved,
		OriginASTInferred, OriginTextMatched, "",
	}
	seen := make(map[edgeHash]string, len(origins))
	for _, o := range origins {
		h := base(o).IdentityHash()
		if prev, dup := seen[h]; dup {
			t.Fatalf("origins %q and %q collided to the same identity hash", prev, o)
		}
		seen[h] = o
	}

	// The Origin-free logical key must NOT determine the identity:
	// two edges with the same keyOf but different Origin differ.
	lsp := base(OriginLSPResolved)
	txt := base(OriginTextMatched)
	if keyOf(lsp) != keyOf(txt) {
		t.Fatal("test setup: edges should share the logical key")
	}
	if lsp.IdentityHash() == txt.IdentityHash() {
		t.Fatal("identity hash must include Origin — same logical key, different Origin must differ")
	}
}

// TestEdgeIdentityHash_DiffersOnLogicalFields confirms IdentityHash
// still discriminates on the logical-key fields, so it is a strict
// superset of edgeKey's discrimination, not a replacement that only
// looks at Origin.
func TestEdgeIdentityHash_DiffersOnLogicalFields(t *testing.T) {
	ref := &Edge{From: "p/a.go::A", To: "p/b.go::B", Kind: EdgeCalls, FilePath: "p/a.go", Line: 12, Origin: OriginASTResolved}
	variants := []*Edge{
		{From: "p/a.go::X", To: "p/b.go::B", Kind: EdgeCalls, FilePath: "p/a.go", Line: 12, Origin: OriginASTResolved},
		{From: "p/a.go::A", To: "p/b.go::X", Kind: EdgeCalls, FilePath: "p/a.go", Line: 12, Origin: OriginASTResolved},
		{From: "p/a.go::A", To: "p/b.go::B", Kind: EdgeReferences, FilePath: "p/a.go", Line: 12, Origin: OriginASTResolved},
		{From: "p/a.go::A", To: "p/b.go::B", Kind: EdgeCalls, FilePath: "p/c.go", Line: 12, Origin: OriginASTResolved},
		{From: "p/a.go::A", To: "p/b.go::B", Kind: EdgeCalls, FilePath: "p/a.go", Line: 99, Origin: OriginASTResolved},
	}
	for i, v := range variants {
		if ref.IdentityHash() == v.IdentityHash() {
			t.Errorf("variant %d: identity hash must differ when a logical-key field differs", i)
		}
	}
}
