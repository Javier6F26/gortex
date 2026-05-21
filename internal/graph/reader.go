package graph

// Reader is the read-only contract every graph consumer (query
// engine, MCP tool handlers, analyzers, resolver introspection) depends
// on. *Graph satisfies it directly; OverlaidView (overlay.go) wraps a
// base Reader plus a per-session overlay layer to deliver a non-
// mutating shadow view for editor-buffer queries.
//
// Mutation methods (AddNode, AddEdge, EvictFile, ReindexEdge, …) live
// on *Graph and are NOT part of this interface. Only the indexer and
// the resolver mutate; everyone else reads, and reads must go through
// Reader so the same call site transparently switches between base
// and overlay views.
//
// New read methods on *Graph should be added here too — keeping the
// surfaces in sync is what guarantees that a tool migrated to read
// through the Reader will keep working for both base and overlay
// queries.
type Reader interface {
	// Identity lookups.
	GetNode(id string) *Node
	GetNodeByQualName(qualName string) *Node
	FindNodesByName(name string) []*Node

	// File / repo scopes.
	GetFileNodes(filePath string) []*Node
	GetRepoNodes(repoPrefix string) []*Node

	// Edge walks.
	GetOutEdges(nodeID string) []*Edge
	GetInEdges(nodeID string) []*Edge

	// Bulk reads — used by analyzers (hotspots, cycles, dead code,
	// communities, …) and by the embedded query engine's whole-graph
	// passes.
	AllNodes() []*Node
	AllEdges() []*Edge

	// Counters & stats.
	NodeCount() int
	EdgeCount() int
	// EdgeIdentityRevisions is the running count of provenance-bearing
	// edge-identity changes — see Graph.EdgeIdentityRevisions.
	EdgeIdentityRevisions() int
	Stats() GraphStats
	RepoStats() map[string]GraphStats
}

// Compile-time assertion that *Graph satisfies Reader. If a new
// Reader method is added without a corresponding *Graph method (or
// the *Graph signature drifts), the build breaks here rather than at
// a far-away callsite.
var _ Reader = (*Graph)(nil)
