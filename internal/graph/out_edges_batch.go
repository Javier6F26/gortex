package graph

// outEdgesBatcher is implemented by backends that can fetch many nodes'
// out-edges in a single query (the disk-backed stores), collapsing the
// per-node N+1 the single-file resolve path would otherwise issue.
type outEdgesBatcher interface {
	GetOutEdgesForNodes(ids []string) map[string][]*Edge
}

// OutEdgesForNodes returns each node's outgoing edges, using the backend's
// batched query when it offers one and falling back to per-node lookups
// otherwise (the in-memory graph, where each lookup is already an O(1) map
// hit). Nodes with no out-edges may be absent from the returned map.
func OutEdgesForNodes(r interface {
	GetOutEdges(nodeID string) []*Edge
}, ids []string) map[string][]*Edge {
	if b, ok := r.(outEdgesBatcher); ok {
		return b.GetOutEdgesForNodes(ids)
	}
	out := make(map[string][]*Edge, len(ids))
	for _, id := range ids {
		if e := r.GetOutEdges(id); len(e) > 0 {
			out[id] = e
		}
	}
	return out
}
