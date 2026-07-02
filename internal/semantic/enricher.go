package semantic

import (
	"github.com/zzet/gortex/internal/graph"
)

// ConfirmEdge upgrades an edge's confidence to EXTRACTED and records the
// semantic source. Origin is set to LSP-grade (lsp_dispatch for interface
// implementations, lsp_resolved for everything else) since only compiler /
// type-system providers call ConfirmEdge.
func ConfirmEdge(e *graph.Edge, provider string) {
	e.Confidence = 1.0
	e.ConfidenceLabel = "EXTRACTED"
	e.Origin = originForSemanticKind(e.Kind)
	if e.Meta == nil {
		e.Meta = make(map[string]any)
	}
	e.Meta["semantic_source"] = provider
}

// RefuteEdge removes a false-positive edge from the graph.
// Returns true if the edge was removed.
func RefuteEdge(g graph.Store, e *graph.Edge) bool {
	return g.RemoveEdge(e.From, e.To, e.Kind)
}

// PersistEdge round-trips an in-place edge mutation (ConfirmEdge, an
// Origin promotion, a Meta stamp) through the backend's edge-attribute
// write path. On the in-memory backend edge reads hand back the live
// *Edge pointer, so the mutation is already durable and this is a
// no-op. Disk backends return detached row copies — without this
// round-trip every enrichment-pass promotion silently evaporates and
// the read path keeps serving the stale heuristic tier.
func PersistEdge(g graph.Store, e *graph.Edge) {
	if w, ok := g.(graph.EdgePersister); ok {
		w.PersistEdgeAttributes(e)
	}
}

// AddSemanticEdge adds a new edge discovered by semantic analysis. Origin is
// tagged LSP-grade (see ConfirmEdge).
func AddSemanticEdge(g graph.Store, from, to string, kind graph.EdgeKind, filePath string, line int, provider string) *graph.Edge {
	e := &graph.Edge{
		From:            from,
		To:              to,
		Kind:            kind,
		FilePath:        filePath,
		Line:            line,
		Confidence:      1.0,
		ConfidenceLabel: "EXTRACTED",
		Origin:          originForSemanticKind(kind),
		Meta: map[string]any{
			"semantic_source": provider,
		},
	}
	g.AddEdge(e)
	return e
}

// originForSemanticKind maps edge kind to the appropriate LSP-grade tier.
// Interface → implementation is a dispatch resolution (one step less direct
// than a literal target match), so it gets lsp_dispatch; direct target
// references get lsp_resolved. Method overrides are method-level
// dispatch — same tier as EdgeImplements.
func originForSemanticKind(kind graph.EdgeKind) string {
	if kind == graph.EdgeImplements || kind == graph.EdgeOverrides {
		return graph.OriginLSPDispatch
	}
	return graph.OriginLSPResolved
}

// EnrichNodeMeta sets semantic type information on a node.
func EnrichNodeMeta(n *graph.Node, key string, value any, provider string) {
	if n.Meta == nil {
		n.Meta = make(map[string]any)
	}
	n.Meta[key] = value
	n.Meta["semantic_source"] = provider
}

// FindMatchingEdge searches for an existing edge between two nodes of a given kind.
func FindMatchingEdge(g graph.Store, from, to string, kind graph.EdgeKind) *graph.Edge {
	edges := g.GetOutEdges(from)
	for _, e := range edges {
		if e.To == to && e.Kind == kind {
			return e
		}
	}
	return nil
}

// FindEdgeByTarget searches for an edge from a node to a target with any kind.
func FindEdgeByTarget(g graph.Store, from, to string) *graph.Edge {
	edges := g.GetOutEdges(from)
	for _, e := range edges {
		if e.To == to {
			return e
		}
	}
	return nil
}

// NodesByLanguage returns all nodes in the graph that match the given language.
func NodesByLanguage(g graph.Store, language string) []*graph.Node {
	var result []*graph.Node
	for _, n := range g.AllNodes() {
		if n.Language == language {
			result = append(result, n)
		}
	}
	return result
}

// EdgesByLanguage returns all edges whose source node matches the given language.
func EdgesByLanguage(g graph.Store, language string) []*graph.Edge {
	var result []*graph.Edge
	for _, e := range g.AllEdges() {
		fromNode := g.GetNode(e.From)
		if fromNode != nil && fromNode.Language == language {
			result = append(result, e)
		}
	}
	return result
}
