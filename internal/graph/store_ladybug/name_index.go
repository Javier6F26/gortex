package store_ladybug

import (
	"strings"
	"sync"
	"sync/atomic"

	"github.com/zzet/gortex/internal/graph"
)

// nameIndex is a denormalised lookup from lowercased Node.Name →
// []*graph.Node.
//
// The codedb playbook calls this the "flat symbol map": a single
// hash hit replaces a graph walk + a BM25 round-trip. For Gortex it
// serves two hot paths:
//
//  1. SearchSymbols tier-0 — identifier queries return exact matches
//     in O(1), skipping FTS entirely. Multi-word queries fall through
//     to FTS with no recall loss.
//  2. FindNodesByName / FindNodesByNameInRepo — the resolver's name-
//     to-candidates lookup. Pre-cache, every per-edge resolver pass
//     paid a Cypher round-trip; on a 100k-edge multi-repo graph that
//     was the warmup bottleneck. The cache is on the hot path of
//     every resolveMethodCall / resolveFunctionCall, so it must
//     deliver a full Node slice without a follow-up cgo fetch.
//
// Population is incremental: AddNode / addNodesUnwindLocked /
// copyBulkLocked all funnel through addNode / addNodes so a steady-
// state per-file update keeps the cache fresh. A lazy bootstrap
// runs on the first lookup if the store opened with disk-resident
// rows the live process never observed — typical after a daemon
// restart.
//
// Maintenance is best-effort: removeByPrefix runs on per-repo
// SymbolFTS wipes so a re-indexed repo's stale entries don't leak
// into tier-0.
type nameIndex struct {
	mu  sync.RWMutex
	byN map[string][]*graph.Node // lower(name) → nodes

	bootstrapped atomic.Bool
	bootstrapMu  sync.Mutex
}

// newNameIndex returns an empty index. Bootstrap fires lazily on
// the first lookup.
func newNameIndex() *nameIndex {
	return &nameIndex{byN: make(map[string][]*graph.Node, 1024)}
}

// addNode is the single-node entry point used by upsertNodeLocked.
// Skips low-value kinds so per-file updates don't flood the cache
// with locals/params.
func (idx *nameIndex) addNode(n *graph.Node) {
	if idx == nil || n == nil || n.Name == "" || n.ID == "" {
		return
	}
	if isLowValueForNameLookup(n.Kind) {
		return
	}
	key := strings.ToLower(n.Name)
	idx.mu.Lock()
	defer idx.mu.Unlock()
	existing := idx.byN[key]
	for _, e := range existing {
		if e.ID == n.ID {
			return
		}
	}
	idx.byN[key] = append(existing, n)
}

// addNodes batches addNode calls so callers iterating a node slice
// (AddBatch, copyBulkLocked) don't pay the per-call lock acquire
// cost.
func (idx *nameIndex) addNodes(nodes []*graph.Node) {
	if idx == nil || len(nodes) == 0 {
		return
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	for _, n := range nodes {
		if n == nil || n.Name == "" || n.ID == "" {
			continue
		}
		if isLowValueForNameLookup(n.Kind) {
			continue
		}
		key := strings.ToLower(n.Name)
		existing := idx.byN[key]
		dup := false
		for _, e := range existing {
			if e.ID == n.ID {
				dup = true
				break
			}
		}
		if !dup {
			idx.byN[key] = append(existing, n)
		}
	}
}

// isLowValueForNameLookup reports whether a node kind has so many
// identical-name occurrences per repo that adding them to the flat
// name index would balloon memory and slow tier-0 lookups without
// giving the resolver useful symbol-binding targets.
func isLowValueForNameLookup(k graph.NodeKind) bool {
	switch k {
	case graph.KindLocal, graph.KindParam, graph.KindFile,
		graph.KindImport, graph.KindGenericParam, graph.KindBuiltin,
		graph.KindClosure:
		return true
	}
	return false
}

// removeByPrefix drops every (name → node) entry whose Node.ID
// matches prefix. Called from the per-repo wipe paths so a re-
// indexed repo's stale entries don't leak into the tier-0 fast
// path. Iterating the entire map is acceptable because removeByPrefix
// runs only on repo-level reset (e.g. before BulkUpsertSymbolFTS's
// per-repo wipe), not on the steady-state hot path.
func (idx *nameIndex) removeByPrefix(prefix string) {
	if idx == nil || prefix == "" {
		return
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	for key, nodes := range idx.byN {
		kept := nodes[:0]
		for _, n := range nodes {
			if !strings.HasPrefix(n.ID, prefix) {
				kept = append(kept, n)
			}
		}
		if len(kept) == 0 {
			delete(idx.byN, key)
		} else {
			idx.byN[key] = kept
		}
	}
}

// lookupNodes returns the nodes whose lowercased Name equals
// strings.ToLower(name). Returns nil on miss. Caller must NOT
// mutate the returned slice's nodes — they are the live cache
// entries shared with the rest of the daemon.
func (idx *nameIndex) lookupNodes(name string) []*graph.Node {
	if idx == nil || name == "" {
		return nil
	}
	key := strings.ToLower(name)
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	nodes := idx.byN[key]
	if len(nodes) == 0 {
		return nil
	}
	out := make([]*graph.Node, len(nodes))
	copy(out, nodes)
	return out
}

// lookup retains the original ID-slice contract for the
// SearchSymbols path that only wants IDs (it builds graph.SymbolHit
// records keyed by ID). Returns a defensive copy.
func (idx *nameIndex) lookup(name string) []string {
	nodes := idx.lookupNodes(name)
	if len(nodes) == 0 {
		return nil
	}
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.ID)
	}
	return out
}

// populated reports whether the index holds any entries — true after a
// cold load's incremental fill (addNodes via copyBulkLocked), false on a
// fresh warm-restart open before the lazy bootstrap. Batch callers use it
// to take the in-memory path WITHOUT calling bootstrap (whose concurrent
// Cypher scan crashed warmup — see FindNodesByName).
func (idx *nameIndex) populated() bool {
	if idx == nil {
		return false
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.byN) > 0
}

// isIdentifierQuery reports whether a query looks like a literal
// symbol name (no whitespace, no path separators, no dots, no
// colons). Tier-0 fast path engages only on such queries; multi-
// token / path / qualified queries always go to FTS.
func isIdentifierQuery(q string) bool {
	if q == "" {
		return false
	}
	for _, r := range q {
		switch r {
		case ' ', '\t', '\n', '/', '.', ':', ',':
			return false
		}
	}
	return true
}

// bootstrap populates the index from a single Cypher scan of the
// Node table, fetching the full row so callers don't need a follow-
// up GetNodesByIDs. Filters out low-value kinds at the engine to
// skip the cgo round-trip cost on locals/params (millions of rows
// in a large multi-repo workspace).
//
// Runs once per Store lifetime on the first lookup that finds an
// empty map — typical after a daemon restart against a warm on-disk
// store where nodes exist but the live process hasn't routed any
// through AddNode/AddBatch yet.
//
// Errors during scan are non-fatal: the index stays empty and
// callers fall through to the Cypher path.
func (idx *nameIndex) bootstrap(s *Store) {
	if idx == nil {
		return
	}
	if idx.bootstrapped.Load() {
		return
	}
	idx.bootstrapMu.Lock()
	defer idx.bootstrapMu.Unlock()
	if idx.bootstrapped.Load() {
		return
	}
	// Fetch full Node rows so the bootstrap-restored cache matches
	// what addNodes builds incrementally. Each row pays the cgo +
	// rowToNode cost once; subsequent lookups are O(1) in-memory.
	//
	// The kind filter is pushed into Cypher so locals (typically
	// 70%+ of all nodes) never cross the cgo boundary. On a 600k-
	// node Linux-scale graph this drops bootstrap time from
	// 6-10 s to < 1 s.
	const q = `MATCH (n:Node) WHERE n.name <> '' AND n.kind IN ['function','method','type','interface','contract','constant','variable','field','module','package','enum_member','table','column','config_key','flag','event','migration','fixture','todo','team','license','release','doc'] RETURN ` + nodeReturnCols
	rows, err := querySelectSafe(s, q, nil)
	if err != nil || len(rows) == 0 {
		idx.bootstrapped.Store(true)
		return
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	for _, r := range rows {
		n := rowToNode(r)
		if n == nil || n.Name == "" || n.ID == "" {
			continue
		}
		key := strings.ToLower(n.Name)
		existing := idx.byN[key]
		dup := false
		for _, e := range existing {
			if e.ID == n.ID {
				dup = true
				break
			}
		}
		if !dup {
			idx.byN[key] = append(existing, n)
		}
	}
	idx.bootstrapped.Store(true)
}
