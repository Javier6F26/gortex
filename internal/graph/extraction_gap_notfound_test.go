package graph

import (
	"strings"
	"testing"
)

// TestCaveatForZeroEdge_NotFoundMessage asserts a queried id that is not in
// the graph (e.g. mistyped or missing its repo prefix) gets a caveat that
// points at the id — while still carrying the untrustworthy class so a
// safety gate trips — and that an existing zero-edge symbol keeps the
// extraction-gap message.
func TestCaveatForZeroEdge_NotFoundMessage(t *testing.T) {
	g := New()
	g.AddNode(&Node{ID: "repo/a.go::Lonely", Kind: KindFunction, Name: "Lonely"}) // exists, no edges

	notFound := CaveatForZeroEdge(g, "a.go::Missing")
	if notFound == nil || notFound.Class != ZeroEdgePossibleExtractionGap {
		t.Fatalf("a not-found id must still carry the untrustworthy class; got %+v", notFound)
	}
	if !strings.Contains(notFound.Message, "repo prefix") {
		t.Errorf("not-found message should point at the id/prefix; got %q", notFound.Message)
	}

	existing := CaveatForZeroEdge(g, "repo/a.go::Lonely")
	if existing == nil {
		t.Fatal("an existing zero-edge symbol should still carry a caveat")
	}
	if strings.Contains(existing.Message, "repo prefix") {
		t.Errorf("an existing zero-edge symbol should get the extraction-gap message, not the not-found one; got %q", existing.Message)
	}
}
