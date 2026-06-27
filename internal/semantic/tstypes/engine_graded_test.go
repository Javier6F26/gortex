package tstypes

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

const gradedTestProvider = "tstypes-test"

// newGradedApplier builds a minimal applier over a fresh graph plus a
// caller->target function pair, for direct exercises of addASTEdge's
// confidence band and resolution-strategy plumbing.
func newGradedApplier(t *testing.T) (*applier, *graph.Graph, *graph.Node, *graph.Node) {
	t.Helper()
	g := graph.New()
	caller := &graph.Node{ID: "p/a.go::Caller", Name: "Caller", Kind: graph.KindFunction, FilePath: "p/a.go"}
	target := &graph.Node{ID: "p/a.go::Target", Name: "Target", Kind: graph.KindFunction, FilePath: "p/a.go"}
	g.AddNode(caller)
	g.AddNode(target)
	return newApplier(g, nil, gradedTestProvider), g, caller, target
}

// countCallsEdges counts the calls-edges between two node ids — used to
// assert a graded emission never minted a duplicate.
func countCallsEdges(g *graph.Graph, from, to string) int {
	n := 0
	for _, e := range g.GetOutEdges(from) {
		if e.Kind == graph.EdgeCalls && e.To == to {
			n++
		}
	}
	return n
}

// TestAddASTEdge_InferredBand proves the graded band end-to-end: an edge
// emitted via the inferred strategy carries the lower confidence and the
// resolution_strategy label, while staying AST-grade (never an LSP
// origin) and supplemental.
func TestAddASTEdge_InferredBand(t *testing.T) {
	a, _, caller, target := newGradedApplier(t)

	e := a.addASTEdge(caller.ID, target.ID, graph.EdgeCalls, "p/a.go", 10, strategyInferred, inferredConfidence)
	if e == nil {
		t.Fatal("addASTEdge returned nil")
	}
	if e.Confidence != inferredConfidence {
		t.Errorf("confidence = %v, want %v", e.Confidence, inferredConfidence)
	}
	if got := e.Meta["resolution_strategy"]; got != string(strategyInferred) {
		t.Errorf("resolution_strategy = %v, want %q", got, string(strategyInferred))
	}
	// Inferred edges stay OriginASTResolved — the sanctioned lower band
	// is a confidence/label distinction, not an origin downgrade or an
	// LSP escalation.
	if e.Origin != graph.OriginASTResolved {
		t.Errorf("origin = %q, want %q", e.Origin, graph.OriginASTResolved)
	}
	if e.Meta["semantic_source"] != gradedTestProvider {
		t.Errorf("semantic_source = %v, want %q", e.Meta["semantic_source"], gradedTestProvider)
	}
	// This engine only ever emits supplemental edges.
	if !(&Provider{}).Supplemental() {
		t.Error("provider unexpectedly not supplemental")
	}
}

// TestAddASTEdge_InferredYieldsToLSPEdge is the critical safety test: a
// 0.7 inferred emission over a pre-existing compiler-grade edge on the
// same (from,to,kind) must NOT overwrite it — the stronger edge survives
// untouched and no duplicate is minted.
func TestAddASTEdge_InferredYieldsToLSPEdge(t *testing.T) {
	a, g, caller, target := newGradedApplier(t)

	strong := &graph.Edge{
		From:       caller.ID,
		To:         target.ID,
		Kind:       graph.EdgeCalls,
		FilePath:   "p/a.go",
		Line:       10,
		Confidence: 1.0,
		Origin:     graph.OriginLSPResolved,
		Meta:       map[string]any{"semantic_source": "lsp-gopls"},
	}
	g.AddEdge(strong)

	got := a.addASTEdge(caller.ID, target.ID, graph.EdgeCalls, "p/a.go", 10, strategyInferred, inferredConfidence)

	if got.Origin != graph.OriginLSPResolved {
		t.Errorf("origin = %q, want %q (stronger edge must survive)", got.Origin, graph.OriginLSPResolved)
	}
	if got.Confidence != 1.0 {
		t.Errorf("confidence = %v, want 1.0 (no downgrade)", got.Confidence)
	}
	if _, stamped := got.Meta["resolution_strategy"]; stamped {
		t.Error("stronger edge was stamped with resolution_strategy; want untouched")
	}
	if n := countCallsEdges(g, caller.ID, target.ID); n != 1 {
		t.Errorf("calls edges = %d, want 1 (no duplicate)", n)
	}
}

// TestAddASTEdge_InferredYieldsToDirectASTEdge proves the same-tier
// safety case: a direct 0.95 AST edge (OriginASTResolved rank, higher
// confidence than the inferred band) also wins — the inferred emission
// yields rather than downgrading it.
func TestAddASTEdge_InferredYieldsToDirectASTEdge(t *testing.T) {
	a, g, caller, target := newGradedApplier(t)

	direct := a.addASTEdge(caller.ID, target.ID, graph.EdgeCalls, "p/a.go", 10, strategyDirect, astConfidence)
	if direct.Confidence != astConfidence {
		t.Fatalf("seed confidence = %v, want %v", direct.Confidence, astConfidence)
	}

	got := a.addASTEdge(caller.ID, target.ID, graph.EdgeCalls, "p/a.go", 10, strategyInferred, inferredConfidence)
	if got.Confidence != astConfidence {
		t.Errorf("confidence = %v, want %v (direct edge must survive)", got.Confidence, astConfidence)
	}
	if _, stamped := got.Meta["resolution_strategy"]; stamped {
		t.Error("direct edge was downgraded to an inferred label; want untouched")
	}
	if n := countCallsEdges(g, caller.ID, target.ID); n != 1 {
		t.Errorf("calls edges = %d, want 1 (no duplicate)", n)
	}
}

// TestAddASTEdge_DirectPathUnchanged pins the default path: it still
// produces an OriginASTResolved edge at astConfidence with the provider
// stamp and NO resolution_strategy label.
func TestAddASTEdge_DirectPathUnchanged(t *testing.T) {
	a, _, caller, target := newGradedApplier(t)

	e := a.addASTEdge(caller.ID, target.ID, graph.EdgeCalls, "p/a.go", 10, strategyDirect, astConfidence)
	if e.Confidence != astConfidence {
		t.Errorf("confidence = %v, want %v", e.Confidence, astConfidence)
	}
	if e.Origin != graph.OriginASTResolved {
		t.Errorf("origin = %q, want %q", e.Origin, graph.OriginASTResolved)
	}
	if e.Meta["semantic_source"] != gradedTestProvider {
		t.Errorf("semantic_source = %v, want %q", e.Meta["semantic_source"], gradedTestProvider)
	}
	if _, stamped := e.Meta["resolution_strategy"]; stamped {
		t.Error("direct path stamped resolution_strategy; want absent")
	}
}
