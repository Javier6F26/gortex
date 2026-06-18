package resolver

import (
	"sort"

	"github.com/zzet/gortex/internal/graph"
)

// closureCollectionVia marks a synthesized closure-collection dispatch edge.
const closureCollectionVia = "closure.collection"

// ccFanoutCap skips a collection field with more dispatchers or registrars than
// this — a generic field name shared across unrelated classes would otherwise
// fan out into noise.
const ccFanoutCap = 8

// ResolveClosureCollectionCalls is the speculative framework-dispatch
// synthesizer for closure-collection dynamic dispatch (Swift-first). A method
// iterates a collection property invoking each element
// (`prop.forEach { $0() }`); another method appends a closure to the same-named
// property (`prop.append(...)`). The Swift extractor stamps the dispatcher with
// Meta["cc_dispatch_field"] and the registrar with Meta["cc_append_field"].
// This pass pairs them globally by field name — cross-file and cross-class by
// design (a base class iterating a collection its subclass appends to) — and
// synthesizes a dispatcher → registrar edge so a flow reaches the registration
// site, where the appended closure's body and its callers live.
//
// Speculative and low-recall: the dispatcher's element-invoke is the gate, so a
// repo with no closure-collection dispatch yields zero edges regardless of how
// many append sites it has; pairing is fan-out capped. Edges ride at
// OriginSpeculative and carry synthesizer provenance; graph.AddEdge dedupes and
// graph.EvictFile drops them on reindex.
//
// Returns the number of closure-collection dispatch edges synthesized.
func ResolveClosureCollectionCalls(g graph.Store) int {
	if g == nil {
		return 0
	}

	dispatchersByField := map[string][]*graph.Node{}
	registrarsByField := map[string][]*graph.Node{}
	for _, n := range nodesByKindsOrAll(g, graph.KindMethod, graph.KindFunction) {
		if n == nil || n.Meta == nil {
			continue
		}
		if f, _ := n.Meta["cc_dispatch_field"].(string); f != "" {
			dispatchersByField[f] = append(dispatchersByField[f], n)
		}
		if f, _ := n.Meta["cc_append_field"].(string); f != "" {
			registrarsByField[f] = append(registrarsByField[f], n)
		}
	}
	if len(dispatchersByField) == 0 {
		return 0
	}

	fields := make([]string, 0, len(dispatchersByField))
	for f := range dispatchersByField {
		fields = append(fields, f)
	}
	sort.Strings(fields)

	var batch []*graph.Edge
	synthesized := 0
	for _, field := range fields {
		disps := dispatchersByField[field]
		regs := registrarsByField[field]
		if len(regs) == 0 {
			continue
		}
		if len(disps) > ccFanoutCap || len(regs) > ccFanoutCap {
			continue
		}
		for _, d := range disps {
			for _, r := range regs {
				if d.ID == r.ID {
					continue
				}
				batch = append(batch, closureCollectionEdge(d, r, field))
				synthesized++
			}
		}
	}

	for _, e := range batch {
		g.AddEdge(e)
	}
	return synthesized
}

// closureCollectionEdge builds one dispatcher→registrar speculative edge.
func closureCollectionEdge(from, to *graph.Node, field string) *graph.Edge {
	return &graph.Edge{
		From:            from.ID,
		To:              to.ID,
		Kind:            graph.EdgeCalls,
		FilePath:        from.FilePath,
		Line:            from.StartLine,
		Confidence:      0.4,
		ConfidenceLabel: graph.ConfidenceLabelFor(graph.EdgeCalls, 0.4),
		Origin:          graph.OriginSpeculative,
		Meta: map[string]any{
			"via":             closureCollectionVia,
			"channel_field":   field,
			"speculative":     true,
			MetaSynthesizedBy: SynthClosureCollection,
			MetaProvenance:    ProvenanceHeuristic,
		},
	}
}
