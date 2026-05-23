// Package store_bolt provides a bbolt-backed implementation of
// graph.Store. The on-disk layout is documented here as the source of
// truth; methods in store.go consult these bucket names.
//
// Schema (bbolt buckets, all top-level):
//
//	nodes              key=nodeID                  value=gob(Node)
//	edges              key=edgeKeyBytes            value=gob(Edge)
//	idx_node_kind      key=kind\x00nodeID          value=empty
//	idx_node_file      key=filePath\x00nodeID      value=empty
//	idx_node_repo      key=repoPrefix\x00nodeID    value=empty
//	idx_node_name      key=name\x00nodeID          value=empty
//	idx_node_qualname  key=qualName                value=nodeID
//	idx_edge_out       key=fromID\x00edgeKeyBytes  value=empty
//	idx_edge_in        key=toID\x00edgeKeyBytes    value=empty
//	meta               misc counters (edge_identity_revisions, ...)
//
// edgeKeyBytes is a stable binary encoding of (from, to, kind, file, line).
// See edgeKey() in store.go for the exact encoding. The encoding pairs
// each variable-length string with a 2-byte big-endian length prefix so
// the byte sequence is uniquely decodable and lexicographically scannable
// by any of its prefixes (e.g. fromID + NUL for "all out-edges of X").
package store_bolt

// Bucket names. Defined as []byte once so callers don't churn allocations
// on every Update / View.
var (
	bucketNodes        = []byte("nodes")
	bucketEdges        = []byte("edges")
	bucketIdxNodeKind  = []byte("idx_node_kind")
	bucketIdxNodeFile  = []byte("idx_node_file")
	bucketIdxNodeRepo  = []byte("idx_node_repo")
	bucketIdxNodeName  = []byte("idx_node_name")
	bucketIdxNodeQual  = []byte("idx_node_qualname")
	bucketIdxEdgeOut   = []byte("idx_edge_out")
	bucketIdxEdgeIn    = []byte("idx_edge_in")
	bucketMeta         = []byte("meta")
)

// All buckets we create on Open. Ordered for determinism in tests.
var allBuckets = [][]byte{
	bucketNodes,
	bucketEdges,
	bucketIdxNodeKind,
	bucketIdxNodeFile,
	bucketIdxNodeRepo,
	bucketIdxNodeName,
	bucketIdxNodeQual,
	bucketIdxEdgeOut,
	bucketIdxEdgeIn,
	bucketMeta,
}

// metaKeyEdgeIdentityRevisions is the bucketMeta key holding the
// monotonically-increasing edge-identity-revision counter (encoded as
// 8 bytes big-endian uint64).
var metaKeyEdgeIdentityRevisions = []byte("edge_identity_revisions")
