package query

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

// TestCallChainDispatchExpansionThroughOverrides proves the polymorphic
// dispatch expansion: a forward call chain that reaches an interface method
// auto-reaches the concrete implementations (and continues through them) ONLY
// when IncludeDispatch is set; the dedicated EdgeOverrides edges are recorded
// as-is (never synthesized into fake calls — so hierarchy queries stay
// precise); and DispatchMinTier gates which overrides qualify by provenance.
func TestCallChainDispatchExpansionThroughOverrides(t *testing.T) {
	g := graph.New()
	for _, id := range []string{"caller", "Iface.Do", "ImplA.Do", "ImplB.Do", "LegacyImpl.Do", "helperA"} {
		g.AddNode(&graph.Node{ID: id, Kind: graph.KindMethod, Name: id})
	}
	g.AddEdge(&graph.Edge{From: "caller", To: "Iface.Do", Kind: graph.EdgeCalls})
	// Two high-tier overrides + one low-tier (text-matched) override.
	g.AddEdge(&graph.Edge{From: "ImplA.Do", To: "Iface.Do", Kind: graph.EdgeOverrides, Origin: graph.OriginASTResolved})
	g.AddEdge(&graph.Edge{From: "ImplB.Do", To: "Iface.Do", Kind: graph.EdgeOverrides, Origin: graph.OriginASTResolved})
	g.AddEdge(&graph.Edge{From: "LegacyImpl.Do", To: "Iface.Do", Kind: graph.EdgeOverrides, Origin: graph.OriginTextMatched})
	// ImplA continues the chain.
	g.AddEdge(&graph.Edge{From: "ImplA.Do", To: "helperA", Kind: graph.EdgeCalls})

	e := NewEngine(g)
	nodeSet := func(sg *SubGraph) map[string]bool {
		out := map[string]bool{}
		for _, n := range sg.Nodes {
			out[n.ID] = true
		}
		return out
	}

	// Baseline: no dispatch expansion — the chain dead-ends at the interface.
	base := nodeSet(e.GetCallChain("caller", QueryOptions{Depth: 5, Limit: 50}))
	if base["ImplA.Do"] || base["helperA"] {
		t.Error("without IncludeDispatch the chain must NOT reach concrete impls")
	}

	// Dispatch-aware: reaches the overriders and continues through them.
	got := nodeSet(e.GetCallChain("caller", QueryOptions{Depth: 5, Limit: 50, IncludeDispatch: true}))
	for _, want := range []string{"Iface.Do", "ImplA.Do", "ImplB.Do", "LegacyImpl.Do", "helperA"} {
		if !got[want] {
			t.Errorf("dispatch expansion did not reach %q", want)
		}
	}

	// The override edges are recorded AS EdgeOverrides, not synthesized calls.
	sg := e.GetCallChain("caller", QueryOptions{Depth: 5, Limit: 50, IncludeDispatch: true})
	var overrideEdges, fakeCalls int
	for _, ed := range sg.Edges {
		if ed.From == "ImplA.Do" && ed.To == "Iface.Do" {
			if ed.Kind == graph.EdgeOverrides {
				overrideEdges++
			}
			if ed.Kind == graph.EdgeCalls {
				fakeCalls++
			}
		}
	}
	if overrideEdges == 0 {
		t.Error("the override edge was not preserved in the result subgraph")
	}
	if fakeCalls != 0 {
		t.Error("a fake `calls` edge was synthesized for an override (precision lost)")
	}

	// min_tier gate: requiring AST-resolved overrides drops the text-matched one.
	gated := nodeSet(e.GetCallChain("caller", QueryOptions{
		Depth: 5, Limit: 50, IncludeDispatch: true, DispatchMinTier: graph.OriginASTResolved,
	}))
	if !gated["ImplA.Do"] || !gated["ImplB.Do"] {
		t.Error("AST-resolved overrides should still expand under DispatchMinTier")
	}
	if gated["LegacyImpl.Do"] {
		t.Error("DispatchMinTier=OriginASTResolved should have gated out the text-matched override")
	}
}
