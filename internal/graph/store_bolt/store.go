package store_bolt

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"errors"
	"fmt"
	"sync"
	"time"

	bbolt "go.etcd.io/bbolt"

	"github.com/zzet/gortex/internal/graph"
)

// Store is a bbolt-backed implementation of graph.Store.
//
// All node/edge state lives on disk in the buckets enumerated in
// bucket_layout.go. The struct holds a single *bbolt.DB plus a tiny
// in-memory mutex used only to serialize the (read-then-write) call
// pattern of SetEdgeProvenance against concurrent identity-revision
// readers — bbolt itself takes care of write serialization, so
// AddNode / AddEdge / AddBatch / EvictFile / EvictRepo do not need
// our help to be race-free.
type Store struct {
	db *bbolt.DB

	// provMu serialises the read-modify-write of SetEdgeProvenance
	// (load the stored edge, compare hashes, rewrite). Without it
	// two concurrent provenance bumps could both observe the
	// pre-change Origin and double-charge the revision counter.
	provMu sync.Mutex
}

// Compile-time assertion: *Store satisfies graph.Store.
var _ graph.Store = (*Store)(nil)

// Open opens (or creates) a bbolt database at path and ensures every
// bucket the schema needs exists.
func Open(path string) (*Store, error) {
	db, err := bbolt.Open(path, 0o600, &bbolt.Options{
		Timeout: 5 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("store_bolt: open %q: %w", path, err)
	}
	if err := db.Update(func(tx *bbolt.Tx) error {
		for _, name := range allBuckets {
			if _, e := tx.CreateBucketIfNotExists(name); e != nil {
				return fmt.Errorf("create bucket %q: %w", name, e)
			}
		}
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the underlying bbolt DB.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// -- encoding helpers ---------------------------------------------------

// encodeNode gob-encodes a node value (we always store by value so the
// caller's pointer cannot mutate persisted state).
func encodeNode(n *graph.Node) ([]byte, error) {
	if n == nil {
		return nil, errors.New("store_bolt: nil node")
	}
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(*n); err != nil {
		return nil, fmt.Errorf("encode node %q: %w", n.ID, err)
	}
	return buf.Bytes(), nil
}

func decodeNode(b []byte) (*graph.Node, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var n graph.Node
	dec := gob.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&n); err != nil {
		return nil, fmt.Errorf("decode node: %w", err)
	}
	return &n, nil
}

func encodeEdge(e *graph.Edge) ([]byte, error) {
	if e == nil {
		return nil, errors.New("store_bolt: nil edge")
	}
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(*e); err != nil {
		return nil, fmt.Errorf("encode edge %s->%s: %w", e.From, e.To, err)
	}
	return buf.Bytes(), nil
}

func decodeEdge(b []byte) (*graph.Edge, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var e graph.Edge
	dec := gob.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&e); err != nil {
		return nil, fmt.Errorf("decode edge: %w", err)
	}
	return &e, nil
}

// edgeKey builds a stable, lexicographically-prefix-scannable binary key
// from the identity tuple (from, to, kind, filePath, line). Each
// variable-length component is prefixed with a 2-byte big-endian length
// so the encoding is uniquely decodable. The single edges bucket is
// keyed by this; the per-endpoint adjacency indexes embed it after the
// endpoint ID and a NUL separator.
func edgeKey(e *graph.Edge) []byte {
	if e == nil {
		return nil
	}
	parts := [][]byte{
		[]byte(e.From),
		[]byte(e.To),
		[]byte(e.Kind),
		[]byte(e.FilePath),
	}
	size := 0
	for _, p := range parts {
		size += 2 + len(p)
	}
	size += 4 // line int32
	buf := make([]byte, 0, size)
	for _, p := range parts {
		var lb [2]byte
		binary.BigEndian.PutUint16(lb[:], uint16(len(p)))
		buf = append(buf, lb[:]...)
		buf = append(buf, p...)
	}
	var line [4]byte
	binary.BigEndian.PutUint32(line[:], uint32(e.Line))
	buf = append(buf, line[:]...)
	return buf
}

// outEdgeIdxKey: fromID + 0x00 + edgeKey
func outEdgeIdxKey(fromID string, ek []byte) []byte {
	buf := make([]byte, 0, len(fromID)+1+len(ek))
	buf = append(buf, fromID...)
	buf = append(buf, 0x00)
	buf = append(buf, ek...)
	return buf
}

// inEdgeIdxKey: toID + 0x00 + edgeKey
func inEdgeIdxKey(toID string, ek []byte) []byte {
	buf := make([]byte, 0, len(toID)+1+len(ek))
	buf = append(buf, toID...)
	buf = append(buf, 0x00)
	buf = append(buf, ek...)
	return buf
}

// scopedKey: prefix + 0x00 + nodeID — used by the kind/file/repo/name
// node indexes whose values are empty (presence is the data).
func scopedKey(prefix, nodeID string) []byte {
	buf := make([]byte, 0, len(prefix)+1+len(nodeID))
	buf = append(buf, prefix...)
	buf = append(buf, 0x00)
	buf = append(buf, nodeID...)
	return buf
}

// -- write paths --------------------------------------------------------

// AddNode inserts or replaces n in the graph. Idempotent on a stable
// (ID) key — re-adding the same node leaves NodeCount unchanged but
// refreshes every per-attribute index (kind, file, repo, name,
// qualname) in case the values drifted.
func (s *Store) AddNode(n *graph.Node) {
	if n == nil || n.ID == "" {
		return
	}
	_ = s.db.Update(func(tx *bbolt.Tx) error {
		return s.putNodeTx(tx, n)
	})
}

// putNodeTx is the shared write path used by AddNode and AddBatch.
// Removes any stale per-attribute index rows from a prior version of
// the same node before writing the fresh ones.
func (s *Store) putNodeTx(tx *bbolt.Tx, n *graph.Node) error {
	if n == nil || n.ID == "" {
		return nil
	}
	nodes := tx.Bucket(bucketNodes)
	idKey := []byte(n.ID)

	// Clear any stale index rows from a prior write under this ID.
	if existing := nodes.Get(idKey); existing != nil {
		old, err := decodeNode(existing)
		if err == nil && old != nil {
			s.removeNodeIndexes(tx, old)
		}
	}

	enc, err := encodeNode(n)
	if err != nil {
		return err
	}
	if err := nodes.Put(idKey, enc); err != nil {
		return err
	}
	return s.addNodeIndexes(tx, n)
}

// addNodeIndexes writes every per-attribute index row for n.
func (s *Store) addNodeIndexes(tx *bbolt.Tx, n *graph.Node) error {
	if n.Kind != "" {
		if err := tx.Bucket(bucketIdxNodeKind).Put(scopedKey(string(n.Kind), n.ID), nil); err != nil {
			return err
		}
	}
	if n.FilePath != "" {
		if err := tx.Bucket(bucketIdxNodeFile).Put(scopedKey(n.FilePath, n.ID), nil); err != nil {
			return err
		}
	}
	if n.RepoPrefix != "" {
		if err := tx.Bucket(bucketIdxNodeRepo).Put(scopedKey(n.RepoPrefix, n.ID), nil); err != nil {
			return err
		}
	}
	if n.Name != "" {
		if err := tx.Bucket(bucketIdxNodeName).Put(scopedKey(n.Name, n.ID), nil); err != nil {
			return err
		}
	}
	if n.QualName != "" {
		if err := tx.Bucket(bucketIdxNodeQual).Put([]byte(n.QualName), []byte(n.ID)); err != nil {
			return err
		}
	}
	return nil
}

// removeNodeIndexes deletes every per-attribute index row for n.
func (s *Store) removeNodeIndexes(tx *bbolt.Tx, n *graph.Node) {
	if n.Kind != "" {
		_ = tx.Bucket(bucketIdxNodeKind).Delete(scopedKey(string(n.Kind), n.ID))
	}
	if n.FilePath != "" {
		_ = tx.Bucket(bucketIdxNodeFile).Delete(scopedKey(n.FilePath, n.ID))
	}
	if n.RepoPrefix != "" {
		_ = tx.Bucket(bucketIdxNodeRepo).Delete(scopedKey(n.RepoPrefix, n.ID))
	}
	if n.Name != "" {
		_ = tx.Bucket(bucketIdxNodeName).Delete(scopedKey(n.Name, n.ID))
	}
	if n.QualName != "" {
		// Only clear the qualname row if it actually points at this node —
		// two distinct nodes with the same QualName can coexist if the
		// caller never enforces uniqueness; we conservatively wipe only
		// the matching row.
		b := tx.Bucket(bucketIdxNodeQual)
		if v := b.Get([]byte(n.QualName)); v != nil && string(v) == n.ID {
			_ = b.Delete([]byte(n.QualName))
		}
	}
}

// AddEdge inserts e, idempotent on the (from, to, kind, filePath, line)
// identity tuple. Re-adding the same logical edge with an upgraded
// Origin replaces the stored value and bumps the identity-revision
// counter.
func (s *Store) AddEdge(e *graph.Edge) {
	if e == nil {
		return
	}
	_ = s.db.Update(func(tx *bbolt.Tx) error {
		_, _, err := s.putEdgeTx(tx, e)
		return err
	})
}

// putEdgeTx is the shared write path used by AddEdge and AddBatch.
// Returns (inserted, originChanged, err) so the caller can update the
// edge-identity-revision counter.
func (s *Store) putEdgeTx(tx *bbolt.Tx, e *graph.Edge) (inserted, originChanged bool, err error) {
	if e == nil {
		return false, false, nil
	}
	ek := edgeKey(e)
	edges := tx.Bucket(bucketEdges)
	prev := edges.Get(ek)
	if prev != nil {
		// An existing edge with the same identity tuple lives here. We
		// replace it in place; the only signal we need to surface is
		// whether the Origin changed.
		old, derr := decodeEdge(prev)
		if derr == nil && old != nil && old.Origin != e.Origin {
			originChanged = true
		}
	} else {
		inserted = true
	}
	enc, eerr := encodeEdge(e)
	if eerr != nil {
		return false, false, eerr
	}
	if err := edges.Put(ek, enc); err != nil {
		return false, false, err
	}
	if err := tx.Bucket(bucketIdxEdgeOut).Put(outEdgeIdxKey(e.From, ek), nil); err != nil {
		return false, false, err
	}
	if err := tx.Bucket(bucketIdxEdgeIn).Put(inEdgeIdxKey(e.To, ek), nil); err != nil {
		return false, false, err
	}
	if originChanged {
		if err := bumpEdgeIdentityRevisions(tx); err != nil {
			return false, false, err
		}
	}
	return inserted, originChanged, nil
}

// AddBatch inserts every node and edge in a single bbolt write
// transaction — the on-disk analogue of *Graph's bulk fast-path.
func (s *Store) AddBatch(nodes []*graph.Node, edges []*graph.Edge) {
	if len(nodes) == 0 && len(edges) == 0 {
		return
	}
	_ = s.db.Update(func(tx *bbolt.Tx) error {
		for _, n := range nodes {
			if n == nil {
				continue
			}
			if err := s.putNodeTx(tx, n); err != nil {
				return err
			}
		}
		for _, e := range edges {
			if e == nil {
				continue
			}
			if _, _, err := s.putEdgeTx(tx, e); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetEdgeProvenance rewrites the persisted edge with a new Origin and
// bumps the identity-revision counter when the change is real. Returns
// false when newOrigin is the same as the stored Origin (no-op).
func (s *Store) SetEdgeProvenance(e *graph.Edge, newOrigin string) bool {
	if e == nil {
		return false
	}
	s.provMu.Lock()
	defer s.provMu.Unlock()
	var changed bool
	_ = s.db.Update(func(tx *bbolt.Tx) error {
		ek := edgeKey(e)
		edges := tx.Bucket(bucketEdges)
		raw := edges.Get(ek)
		if raw == nil {
			return nil
		}
		stored, derr := decodeEdge(raw)
		if derr != nil || stored == nil {
			return derr
		}
		if stored.Origin == newOrigin {
			return nil
		}
		stored.Origin = newOrigin
		// Mirror the in-memory contract: Tier is a pure projection of
		// Origin (graph.ResolvedBy), and we re-derive it only when it
		// was already populated.
		if stored.Tier != "" {
			stored.Tier = graph.ResolvedBy(newOrigin)
		}
		// Also mutate the caller's pointer so the test that inspects
		// `e.Origin` after the call sees the new value (mirrors the
		// in-memory store, which keeps a single pointer per edge).
		e.Origin = newOrigin
		if e.Tier != "" {
			e.Tier = graph.ResolvedBy(newOrigin)
		}
		enc, eerr := encodeEdge(stored)
		if eerr != nil {
			return eerr
		}
		if err := edges.Put(ek, enc); err != nil {
			return err
		}
		if err := bumpEdgeIdentityRevisions(tx); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed
}

// ReindexEdge moves an edge from (From, oldTo) to (From, e.To). Used by
// the indexer after a To-side relink. We delete the old key tuple
// outright and reinsert with the current e — origin/meta are preserved
// because the caller hands us the still-valid struct.
func (s *Store) ReindexEdge(e *graph.Edge, oldTo string) {
	if e == nil {
		return
	}
	_ = s.db.Update(func(tx *bbolt.Tx) error {
		// Build the old key by temporarily swapping To back.
		newTo := e.To
		e.To = oldTo
		oldKey := edgeKey(e)
		e.To = newTo
		// Drop the old edge + its adjacency rows.
		edges := tx.Bucket(bucketEdges)
		_ = edges.Delete(oldKey)
		_ = tx.Bucket(bucketIdxEdgeOut).Delete(outEdgeIdxKey(e.From, oldKey))
		_ = tx.Bucket(bucketIdxEdgeIn).Delete(inEdgeIdxKey(oldTo, oldKey))
		// Insert under the new key.
		_, _, err := s.putEdgeTx(tx, e)
		return err
	})
}

// RemoveEdge drops the edge with the given (from, to, kind) tuple.
// Returns true when something was actually removed. Because the
// identity tuple includes FilePath and Line, multiple edges may share
// the same (from, to, kind); we walk the out-edge index for this from-
// node and delete every match.
func (s *Store) RemoveEdge(from, to string, kind graph.EdgeKind) bool {
	var removed bool
	_ = s.db.Update(func(tx *bbolt.Tx) error {
		outIdx := tx.Bucket(bucketIdxEdgeOut)
		edges := tx.Bucket(bucketEdges)
		inIdx := tx.Bucket(bucketIdxEdgeIn)
		prefix := append([]byte(from), 0x00)
		c := outIdx.Cursor()
		// We can't delete while iterating safely; collect first.
		var toDelete [][]byte
		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			ek := k[len(prefix):]
			raw := edges.Get(ek)
			if raw == nil {
				continue
			}
			e, derr := decodeEdge(raw)
			if derr != nil || e == nil {
				continue
			}
			if e.To == to && e.Kind == kind {
				cp := make([]byte, len(ek))
				copy(cp, ek)
				toDelete = append(toDelete, cp)
			}
		}
		for _, ek := range toDelete {
			if err := edges.Delete(ek); err != nil {
				return err
			}
			if err := outIdx.Delete(outEdgeIdxKey(from, ek)); err != nil {
				return err
			}
			if err := inIdx.Delete(inEdgeIdxKey(to, ek)); err != nil {
				return err
			}
			removed = true
		}
		return nil
	})
	return removed
}

// EvictFile drops every node whose FilePath equals filePath plus every
// edge touching one of those nodes. Returns (nodesRemoved, edgesRemoved).
func (s *Store) EvictFile(filePath string) (int, int) {
	if filePath == "" {
		return 0, 0
	}
	var nRemoved, eRemoved int
	_ = s.db.Update(func(tx *bbolt.Tx) error {
		ids := s.collectIDsByScopedPrefix(tx, bucketIdxNodeFile, filePath)
		nRemoved, eRemoved = s.evictNodesByID(tx, ids)
		return nil
	})
	return nRemoved, eRemoved
}

// EvictRepo drops every node whose RepoPrefix equals repoPrefix plus
// every edge touching one of those nodes.
func (s *Store) EvictRepo(repoPrefix string) (int, int) {
	if repoPrefix == "" {
		return 0, 0
	}
	var nRemoved, eRemoved int
	_ = s.db.Update(func(tx *bbolt.Tx) error {
		ids := s.collectIDsByScopedPrefix(tx, bucketIdxNodeRepo, repoPrefix)
		nRemoved, eRemoved = s.evictNodesByID(tx, ids)
		return nil
	})
	return nRemoved, eRemoved
}

// collectIDsByScopedPrefix walks a scoped index bucket (kind / file /
// repo / name) for the rows whose prefix equals `prefix` and returns
// the node IDs encoded after the NUL separator.
func (s *Store) collectIDsByScopedPrefix(tx *bbolt.Tx, bucketName []byte, prefix string) []string {
	b := tx.Bucket(bucketName)
	if b == nil {
		return nil
	}
	pfx := append([]byte(prefix), 0x00)
	var ids []string
	c := b.Cursor()
	for k, _ := c.Seek(pfx); k != nil && bytes.HasPrefix(k, pfx); k, _ = c.Next() {
		ids = append(ids, string(k[len(pfx):]))
	}
	return ids
}

// evictNodesByID deletes the listed nodes (plus their index rows and
// every adjacent edge). Returns (nodesRemoved, edgesRemoved).
func (s *Store) evictNodesByID(tx *bbolt.Tx, ids []string) (int, int) {
	if len(ids) == 0 {
		return 0, 0
	}
	nodes := tx.Bucket(bucketNodes)
	edges := tx.Bucket(bucketEdges)
	outIdx := tx.Bucket(bucketIdxEdgeOut)
	inIdx := tx.Bucket(bucketIdxEdgeIn)

	idSet := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		idSet[id] = struct{}{}
	}

	nRemoved := 0
	for _, id := range ids {
		raw := nodes.Get([]byte(id))
		if raw == nil {
			continue
		}
		n, derr := decodeNode(raw)
		if derr == nil && n != nil {
			s.removeNodeIndexes(tx, n)
		}
		if err := nodes.Delete([]byte(id)); err != nil {
			continue
		}
		nRemoved++
	}

	// Collect every edge whose endpoint is in idSet — we walk both
	// adjacency indexes so an edge whose endpoints are *both* evicted
	// is still counted exactly once.
	type edgeRow struct {
		key  []byte
		from string
		to   string
	}
	seen := make(map[string]edgeRow)
	collect := func(idx *bbolt.Bucket) {
		c := idx.Cursor()
		for _, id := range ids {
			pfx := append([]byte(id), 0x00)
			for k, _ := c.Seek(pfx); k != nil && bytes.HasPrefix(k, pfx); k, _ = c.Next() {
				ek := k[len(pfx):]
				raw := edges.Get(ek)
				if raw == nil {
					continue
				}
				e, derr := decodeEdge(raw)
				if derr != nil || e == nil {
					continue
				}
				cp := make([]byte, len(ek))
				copy(cp, ek)
				seen[string(cp)] = edgeRow{key: cp, from: e.From, to: e.To}
			}
		}
	}
	collect(outIdx)
	collect(inIdx)

	for _, row := range seen {
		_ = edges.Delete(row.key)
		_ = outIdx.Delete(outEdgeIdxKey(row.from, row.key))
		_ = inIdx.Delete(inEdgeIdxKey(row.to, row.key))
	}
	return nRemoved, len(seen)
}

// -- point lookups ------------------------------------------------------

func (s *Store) GetNode(id string) *graph.Node {
	if id == "" {
		return nil
	}
	var out *graph.Node
	_ = s.db.View(func(tx *bbolt.Tx) error {
		raw := tx.Bucket(bucketNodes).Get([]byte(id))
		if raw == nil {
			return nil
		}
		// Copy the bytes out before decode — bbolt invalidates them
		// once the txn ends, but decoding inside the txn is fine.
		n, derr := decodeNode(raw)
		if derr == nil {
			out = n
		}
		return nil
	})
	return out
}

func (s *Store) GetNodeByQualName(qualName string) *graph.Node {
	if qualName == "" {
		return nil
	}
	var id string
	_ = s.db.View(func(tx *bbolt.Tx) error {
		v := tx.Bucket(bucketIdxNodeQual).Get([]byte(qualName))
		if v != nil {
			id = string(v)
		}
		return nil
	})
	if id == "" {
		return nil
	}
	return s.GetNode(id)
}

// -- name + scope queries ---------------------------------------------

func (s *Store) FindNodesByName(name string) []*graph.Node {
	if name == "" {
		return nil
	}
	var out []*graph.Node
	_ = s.db.View(func(tx *bbolt.Tx) error {
		ids := s.collectIDsByScopedPrefix(tx, bucketIdxNodeName, name)
		out = make([]*graph.Node, 0, len(ids))
		nodes := tx.Bucket(bucketNodes)
		for _, id := range ids {
			raw := nodes.Get([]byte(id))
			if raw == nil {
				continue
			}
			n, derr := decodeNode(raw)
			if derr == nil && n != nil {
				out = append(out, n)
			}
		}
		return nil
	})
	return out
}

func (s *Store) FindNodesByNameInRepo(name, repoPrefix string) []*graph.Node {
	if name == "" {
		return nil
	}
	all := s.FindNodesByName(name)
	if repoPrefix == "" {
		return all
	}
	out := all[:0]
	for _, n := range all {
		if n != nil && n.RepoPrefix == repoPrefix {
			out = append(out, n)
		}
	}
	return out
}

func (s *Store) GetFileNodes(filePath string) []*graph.Node {
	if filePath == "" {
		return nil
	}
	var out []*graph.Node
	_ = s.db.View(func(tx *bbolt.Tx) error {
		ids := s.collectIDsByScopedPrefix(tx, bucketIdxNodeFile, filePath)
		out = make([]*graph.Node, 0, len(ids))
		nodes := tx.Bucket(bucketNodes)
		for _, id := range ids {
			raw := nodes.Get([]byte(id))
			if raw == nil {
				continue
			}
			n, derr := decodeNode(raw)
			if derr == nil && n != nil {
				out = append(out, n)
			}
		}
		return nil
	})
	return out
}

func (s *Store) GetRepoNodes(repoPrefix string) []*graph.Node {
	if repoPrefix == "" {
		return nil
	}
	var out []*graph.Node
	_ = s.db.View(func(tx *bbolt.Tx) error {
		ids := s.collectIDsByScopedPrefix(tx, bucketIdxNodeRepo, repoPrefix)
		out = make([]*graph.Node, 0, len(ids))
		nodes := tx.Bucket(bucketNodes)
		for _, id := range ids {
			raw := nodes.Get([]byte(id))
			if raw == nil {
				continue
			}
			n, derr := decodeNode(raw)
			if derr == nil && n != nil {
				out = append(out, n)
			}
		}
		return nil
	})
	return out
}

// -- edge adjacency ----------------------------------------------------

func (s *Store) GetOutEdges(nodeID string) []*graph.Edge {
	if nodeID == "" {
		return nil
	}
	var out []*graph.Edge
	_ = s.db.View(func(tx *bbolt.Tx) error {
		out = s.collectEdgesByEndpoint(tx, bucketIdxEdgeOut, nodeID)
		return nil
	})
	return out
}

func (s *Store) GetInEdges(nodeID string) []*graph.Edge {
	if nodeID == "" {
		return nil
	}
	var out []*graph.Edge
	_ = s.db.View(func(tx *bbolt.Tx) error {
		out = s.collectEdgesByEndpoint(tx, bucketIdxEdgeIn, nodeID)
		return nil
	})
	return out
}

func (s *Store) collectEdgesByEndpoint(tx *bbolt.Tx, idxBucket []byte, nodeID string) []*graph.Edge {
	idx := tx.Bucket(idxBucket)
	edges := tx.Bucket(bucketEdges)
	prefix := append([]byte(nodeID), 0x00)
	var out []*graph.Edge
	c := idx.Cursor()
	for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
		ek := k[len(prefix):]
		raw := edges.Get(ek)
		if raw == nil {
			continue
		}
		e, derr := decodeEdge(raw)
		if derr == nil && e != nil {
			out = append(out, e)
		}
	}
	return out
}

// -- bulk reads --------------------------------------------------------

func (s *Store) AllNodes() []*graph.Node {
	var out []*graph.Node
	_ = s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(bucketNodes)
		out = make([]*graph.Node, 0, b.Stats().KeyN)
		return b.ForEach(func(_, v []byte) error {
			n, derr := decodeNode(v)
			if derr == nil && n != nil {
				out = append(out, n)
			}
			return nil
		})
	})
	return out
}

func (s *Store) AllEdges() []*graph.Edge {
	var out []*graph.Edge
	_ = s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(bucketEdges)
		out = make([]*graph.Edge, 0, b.Stats().KeyN)
		return b.ForEach(func(_, v []byte) error {
			e, derr := decodeEdge(v)
			if derr == nil && e != nil {
				out = append(out, e)
			}
			return nil
		})
	})
	return out
}

// -- counts and stats --------------------------------------------------

func (s *Store) NodeCount() int {
	var n int
	_ = s.db.View(func(tx *bbolt.Tx) error {
		n = tx.Bucket(bucketNodes).Stats().KeyN
		return nil
	})
	return n
}

func (s *Store) EdgeCount() int {
	var n int
	_ = s.db.View(func(tx *bbolt.Tx) error {
		n = tx.Bucket(bucketEdges).Stats().KeyN
		return nil
	})
	return n
}

func (s *Store) Stats() graph.GraphStats {
	st := graph.GraphStats{
		ByKind:     make(map[string]int),
		ByLanguage: make(map[string]int),
	}
	_ = s.db.View(func(tx *bbolt.Tx) error {
		nodes := tx.Bucket(bucketNodes)
		st.TotalNodes = nodes.Stats().KeyN
		st.TotalEdges = tx.Bucket(bucketEdges).Stats().KeyN
		return nodes.ForEach(func(_, v []byte) error {
			n, derr := decodeNode(v)
			if derr != nil || n == nil {
				return nil
			}
			if n.Kind != "" {
				st.ByKind[string(n.Kind)]++
			}
			if n.Language != "" {
				st.ByLanguage[n.Language]++
			}
			return nil
		})
	})
	return st
}

func (s *Store) RepoStats() map[string]graph.GraphStats {
	out := make(map[string]graph.GraphStats)
	_ = s.db.View(func(tx *bbolt.Tx) error {
		nodes := tx.Bucket(bucketNodes)
		return nodes.ForEach(func(_, v []byte) error {
			n, derr := decodeNode(v)
			if derr != nil || n == nil {
				return nil
			}
			repo := n.RepoPrefix
			st, ok := out[repo]
			if !ok {
				st = graph.GraphStats{
					ByKind:     make(map[string]int),
					ByLanguage: make(map[string]int),
				}
			}
			st.TotalNodes++
			if n.Kind != "" {
				st.ByKind[string(n.Kind)]++
			}
			if n.Language != "" {
				st.ByLanguage[n.Language]++
			}
			out[repo] = st
			return nil
		})
	})
	// Count edges by source node's repo.
	_ = s.db.View(func(tx *bbolt.Tx) error {
		edges := tx.Bucket(bucketEdges)
		nodes := tx.Bucket(bucketNodes)
		return edges.ForEach(func(_, v []byte) error {
			e, derr := decodeEdge(v)
			if derr != nil || e == nil {
				return nil
			}
			raw := nodes.Get([]byte(e.From))
			if raw == nil {
				return nil
			}
			src, derr := decodeNode(raw)
			if derr != nil || src == nil {
				return nil
			}
			st, ok := out[src.RepoPrefix]
			if !ok {
				st = graph.GraphStats{
					ByKind:     make(map[string]int),
					ByLanguage: make(map[string]int),
				}
			}
			st.TotalEdges++
			out[src.RepoPrefix] = st
			return nil
		})
	})
	return out
}

func (s *Store) RepoPrefixes() []string {
	seen := make(map[string]struct{})
	_ = s.db.View(func(tx *bbolt.Tx) error {
		c := tx.Bucket(bucketIdxNodeRepo).Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			// Key shape: prefix + 0x00 + nodeID
			i := bytes.IndexByte(k, 0x00)
			if i <= 0 {
				continue
			}
			seen[string(k[:i])] = struct{}{}
		}
		return nil
	})
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	return out
}

// -- provenance verification ------------------------------------------

func (s *Store) EdgeIdentityRevisions() int {
	var n int
	_ = s.db.View(func(tx *bbolt.Tx) error {
		raw := tx.Bucket(bucketMeta).Get(metaKeyEdgeIdentityRevisions)
		if len(raw) != 8 {
			return nil
		}
		n = int(binary.BigEndian.Uint64(raw))
		return nil
	})
	return n
}

// VerifyEdgeIdentities sanity-checks that every edge in the canonical
// edges bucket is reachable from both the out- and in-adjacency
// indexes. A missing index row signals a corrupted write.
func (s *Store) VerifyEdgeIdentities() error {
	return s.db.View(func(tx *bbolt.Tx) error {
		edges := tx.Bucket(bucketEdges)
		outIdx := tx.Bucket(bucketIdxEdgeOut)
		inIdx := tx.Bucket(bucketIdxEdgeIn)
		return edges.ForEach(func(k, v []byte) error {
			e, derr := decodeEdge(v)
			if derr != nil || e == nil {
				return nil
			}
			if outIdx.Get(outEdgeIdxKey(e.From, k)) == nil {
				return fmt.Errorf("store_bolt: edge %s->%s missing out-index", e.From, e.To)
			}
			if inIdx.Get(inEdgeIdxKey(e.To, k)) == nil {
				return fmt.Errorf("store_bolt: edge %s->%s missing in-index", e.From, e.To)
			}
			return nil
		})
	})
}

// -- memory estimation -------------------------------------------------

func (s *Store) RepoMemoryEstimate(repoPrefix string) graph.RepoMemoryEstimate {
	var est graph.RepoMemoryEstimate
	nodes := s.GetRepoNodes(repoPrefix)
	est.NodeCount = len(nodes)
	for _, n := range nodes {
		est.NodeBytes += nodeBytesEstimate(n)
	}
	// Edge accounting: any edge whose From belongs to repoPrefix counts.
	nodeIDs := make(map[string]struct{}, len(nodes))
	for _, n := range nodes {
		nodeIDs[n.ID] = struct{}{}
	}
	_ = s.db.View(func(tx *bbolt.Tx) error {
		return tx.Bucket(bucketEdges).ForEach(func(_, v []byte) error {
			e, derr := decodeEdge(v)
			if derr != nil || e == nil {
				return nil
			}
			if _, ok := nodeIDs[e.From]; ok {
				est.EdgeCount++
				est.EdgeBytes += edgeBytesEstimate(e)
			}
			return nil
		})
	})
	return est
}

func (s *Store) AllRepoMemoryEstimates() map[string]graph.RepoMemoryEstimate {
	out := make(map[string]graph.RepoMemoryEstimate)
	repoOf := make(map[string]string)
	_ = s.db.View(func(tx *bbolt.Tx) error {
		return tx.Bucket(bucketNodes).ForEach(func(_, v []byte) error {
			n, derr := decodeNode(v)
			if derr != nil || n == nil {
				return nil
			}
			repoOf[n.ID] = n.RepoPrefix
			est := out[n.RepoPrefix]
			est.NodeCount++
			est.NodeBytes += nodeBytesEstimate(n)
			out[n.RepoPrefix] = est
			return nil
		})
	})
	_ = s.db.View(func(tx *bbolt.Tx) error {
		return tx.Bucket(bucketEdges).ForEach(func(_, v []byte) error {
			e, derr := decodeEdge(v)
			if derr != nil || e == nil {
				return nil
			}
			repo, ok := repoOf[e.From]
			if !ok {
				return nil
			}
			est := out[repo]
			est.EdgeCount++
			est.EdgeBytes += edgeBytesEstimate(e)
			out[repo] = est
			return nil
		})
	})
	return out
}

// Per-record byte estimates — these mirror the in-memory store's
// nodeBytes / edgeBytes (struct overhead + string lengths) so the
// numbers stay comparable. Internal helpers, not exported.
const (
	nodeStructOverheadEstimate = uint64(200)
	edgeStructOverheadEstimate = uint64(120)
)

func nodeBytesEstimate(n *graph.Node) uint64 {
	if n == nil {
		return 0
	}
	b := nodeStructOverheadEstimate
	b += uint64(len(n.ID) + len(n.Name) + len(n.QualName) + len(n.FilePath) + len(n.Language) + len(n.RepoPrefix))
	return b
}

func edgeBytesEstimate(e *graph.Edge) uint64 {
	if e == nil {
		return 0
	}
	b := edgeStructOverheadEstimate
	b += uint64(len(e.From) + len(e.To) + len(e.Kind) + len(e.FilePath))
	return b
}

// bumpEdgeIdentityRevisions increments the monotonic counter stored
// in the meta bucket.
func bumpEdgeIdentityRevisions(tx *bbolt.Tx) error {
	b := tx.Bucket(bucketMeta)
	raw := b.Get(metaKeyEdgeIdentityRevisions)
	var n uint64
	if len(raw) == 8 {
		n = binary.BigEndian.Uint64(raw)
	}
	n++
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], n)
	return b.Put(metaKeyEdgeIdentityRevisions, buf[:])
}
