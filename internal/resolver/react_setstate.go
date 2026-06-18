package resolver

import (
	"sort"
	"strings"

	"github.com/zzet/gortex/internal/graph"
)

// reactSetStateVia marks a synthesized React class-component setState→render
// reachability edge.
const reactSetStateVia = "react.setstate"

// ResolveReactSetStateCalls is the framework-dispatch synthesizer for the React
// class-component re-render hop. `this.setState(...)` re-runs the component's
// `render()`, but that hop is React-internal — no static edge — so a flow like
// "event → setState → render → child components" dead-ends at setState even
// though everything after render is call-connected. This pass bridges it: for
// each class that has a `render` method, it links every sibling method whose
// body calls `this.setState(` to that `render`. The setState call is the gate
// that keeps this to React class components — a plain class with a `render`
// method that never calls `this.setState` produces no edge.
//
// Over-approximation by design (every setState method reaches render), full
// recompute and idempotent: edges are re-derived from the call + membership
// metadata, graph.AddEdge dedupes, and graph.EvictFile drops them on reindex.
// Edges ride at ast_inferred and carry synthesizer provenance.
//
// Returns the number of setState→render edges synthesized.
func ResolveReactSetStateCalls(g graph.Store) int {
	if g == nil {
		return 0
	}

	classByMethod := map[string]string{}
	renderByClass := map[string]*graph.Node{}
	for _, n := range nodesByKindsOrAll(g, graph.KindMethod) {
		if n == nil {
			continue
		}
		for _, e := range g.GetOutEdges(n.ID) {
			if e == nil || e.Kind != graph.EdgeMemberOf {
				continue
			}
			classByMethod[n.ID] = e.To
			if n.Name == "render" {
				renderByClass[e.To] = n
			}
			break
		}
	}
	if len(renderByClass) == 0 {
		return 0
	}

	var setStateMethods []*graph.Node
	for _, n := range nodesByKindsOrAll(g, graph.KindMethod) {
		if n == nil {
			continue
		}
		class := classByMethod[n.ID]
		render := renderByClass[class]
		if render == nil || render.ID == n.ID {
			continue
		}
		if !methodCallsSetState(g, n.ID) {
			continue
		}
		setStateMethods = append(setStateMethods, n)
	}
	sort.Slice(setStateMethods, func(i, j int) bool {
		return setStateMethods[i].ID < setStateMethods[j].ID
	})

	var batch []*graph.Edge
	synthesized := 0
	for _, m := range setStateMethods {
		render := renderByClass[classByMethod[m.ID]]
		batch = append(batch, reactSetStateEdge(m, render, classByMethod[m.ID]))
		synthesized++
	}

	for _, e := range batch {
		g.AddEdge(e)
	}
	return synthesized
}

// methodCallsSetState reports whether a method has an outgoing call to
// `this.setState(...)` (the unresolved `*.setState` placeholder, or a resolved
// setState method).
func methodCallsSetState(g graph.Store, methodID string) bool {
	for _, e := range g.GetOutEdges(methodID) {
		if e == nil || e.Kind != graph.EdgeCalls {
			continue
		}
		if isSetStateTarget(e.To) {
			return true
		}
	}
	return false
}

// isSetStateTarget matches a call target that names React's setState.
func isSetStateTarget(to string) bool {
	return strings.HasSuffix(to, ".setState") ||
		strings.HasSuffix(to, "::setState") ||
		to == "unresolved::setState"
}

// reactSetStateEdge builds one setState-method → render synthesized edge.
func reactSetStateEdge(from, render *graph.Node, class string) *graph.Edge {
	return &graph.Edge{
		From:            from.ID,
		To:              render.ID,
		Kind:            graph.EdgeCalls,
		FilePath:        from.FilePath,
		Line:            from.StartLine,
		Confidence:      0.6,
		ConfidenceLabel: graph.ConfidenceLabelFor(graph.EdgeCalls, 0.6),
		Origin:          graph.OriginASTInferred,
		Meta: map[string]any{
			"via":             reactSetStateVia,
			"component_class": class,
			MetaSynthesizedBy: SynthReactSetState,
			MetaProvenance:    ProvenanceHeuristic,
		},
	}
}
