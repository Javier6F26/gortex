package store_pg

import (
	"fmt"

	"github.com/zzet/gortex/internal/graph"
)

// This file implements graph.SymbolSearcher + graph.SymbolBundleSearcher
// on the PostgreSQL backend using pg_trgm trigram similarity search on
// nodes.name with a GIN index. It replaces SQLite's FTS5 virtual table.
//
// Design:
//
//   - Symbol names are indexed by a GIN trigram index on nodes(name).
//     pg_trgm's similarity() function provides BM25-equivalent relevance
//     scoring with typo tolerance.
//
//   - Tier 0: exact name match short-circuits FTS for identifier queries
//     (no whitespace / path separators), returning the matched symbols
//     with a fixed dominant score. Misses fall through to pg_trgm.
//
//   - Bulk upsert replaces rows per repo prefix atomically.
//
//   - Symbol bundles compose the same hit list with batched node + in/out
//     edge fetches the rerank pipeline reads from.

// Compile-time assertions: *Store satisfies the symbol-search capabilities.
var (
	_ graph.SymbolSearcher       = (*Store)(nil)
	_ graph.SymbolBundleSearcher = (*Store)(nil)
)

// UpsertSymbolFTS records (or replaces) the pre-tokenised text for a symbol.
// Since pg_trgm indexes the nodes.name column directly, "upserting" FTS data
// is simply ensuring the node has the correct name — which AddNode already
// handles via ON CONFLICT DO UPDATE. This method is a no-op because the node
// table's name column IS the search index.
func (s *Store) UpsertSymbolFTS(nodeID, tokens string) error {
	if s.refuseWrite("UpsertSymbolFTS") { return ErrReadOnlyStore }
	return nil
}

// BuildSymbolIndex ensures the pg_trgm GIN index exists. Idempotent.
func (s *Store) BuildSymbolIndex() error {
	if s.refuseWrite("BuildSymbolIndex") { return ErrReadOnlyStore }
	_, err := s.pool.Exec(s.ctx, `CREATE INDEX IF NOT EXISTS idx_nodes_name_trgm ON nodes USING GIN (name gin_trgm_ops)`)
	return err
}

// SearchSymbols runs a pg_trgm similarity query and returns hits ordered
// by score descending. Tier 0: exact name matches short-circuit for simple
// identifier queries.
func (s *Store) SearchSymbols(query string, limit int) ([]graph.SymbolHit, error) {
	if query == "" || limit <= 0 {
		return nil, nil
	}

	// Tier 0: exact name match for identifier queries (no spaces/slashes).
	if !hasWhitespaceOrSlash(query) {
		exactHits, err := s.exactNameSearch(query, limit)
		if err != nil {
			return nil, err
		}
		if len(exactHits) > 0 {
			return exactHits, nil
		}
	}

	// pg_trgm similarity search.
	return s.trgmSearch(query, limit)
}

// exactNameSearch returns exact name matches with a dominant score.
func (s *Store) exactNameSearch(name string, limit int) ([]graph.SymbolHit, error) {
	rows, err := s.pool.Query(s.ctx,
		`SELECT id, 100.0 AS score FROM nodes WHERE name = $1 ORDER BY id LIMIT $2`, name, limit)
	if err != nil {
		return nil, fmt.Errorf("store_pg: exact name search: %w", err)
	}
	defer rows.Close()

	var hits []graph.SymbolHit
	for rows.Next() {
		var h graph.SymbolHit
		if err := rows.Scan(&h.NodeID, &h.Score); err != nil {
			return hits, err
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// trgmSearch runs a pg_trgm trigram similarity search.
func (s *Store) trgmSearch(query string, limit int) ([]graph.SymbolHit, error) {
	rows, err := s.pool.Query(s.ctx,
		`SELECT id, similarity(name, $1) AS score
		 FROM nodes
		 WHERE name % $1
		 ORDER BY score DESC
		 LIMIT $2`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("store_pg: trgm search: %w", err)
	}
	defer rows.Close()

	var hits []graph.SymbolHit
	for rows.Next() {
		var h graph.SymbolHit
		if err := rows.Scan(&h.NodeID, &h.Score); err != nil {
			return hits, err
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// hasWhitespaceOrSlash reports whether s contains whitespace or a slash.
func hasWhitespaceOrSlash(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' || c == '/' || c == '\\' {
			return true
		}
	}
	return false
}

// SearchSymbolBundles composes the same hit list with batched node + in/out
// edge fetches the rerank pipeline reads from. Results are cached per
// package: when the daemon's analysis-pass fingerprint for a package is
// unchanged from the cached one, the edge fetches for that package are
// skipped (the node fetch always runs — it's a single batched query and
// provides the FilePath needed to determine the package key).
func (s *Store) SearchSymbolBundles(query string, limit int) ([]graph.SymbolBundle, error) {
	hits, err := s.SearchSymbols(query, limit)
	if err != nil || len(hits) == 0 {
		return nil, err
	}

	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.NodeID
	}

	// Fetch all nodes first — we need FilePath to determine the
	// package key for caching.
	nodes := s.GetNodesByIDs(ids)

	// Separate hits into cached vs miss per package.
	// The cache stores bundles per package key, so we group by pkgKey.
	type pkgGroup struct {
		nodes []*graph.Node
		hits  []graph.SymbolHit
	}
	groups := map[string]*pkgGroup{}
	hitOrder := make([]string, 0, len(hits))
	for _, h := range hits {
		n := nodes[h.NodeID]
		if n == nil {
			continue
		}
		pkgKey := bundlePackageKey(n.FilePath)
		if _, ok := groups[pkgKey]; !ok {
			groups[pkgKey] = &pkgGroup{}
			hitOrder = append(hitOrder, pkgKey)
		}
		groups[pkgKey].nodes = append(groups[pkgKey].nodes, n)
		groups[pkgKey].hits = append(groups[pkgKey].hits, h)
	}

	var bundles []graph.SymbolBundle
	var missIDs []string
	var missNodes []*graph.Node
	missNodeHits := make([]graph.SymbolHit, 0, len(hits)) // hits for which we need fresh edges

	for _, pkgKey := range hitOrder {
		g := groups[pkgKey]
		if s.bundles == nil {
			// No cache — collect everything for direct edge query.
			for _, n := range g.nodes {
				missIDs = append(missIDs, n.ID)
			}
			missNodes = append(missNodes, g.nodes...)
			missNodeHits = append(missNodeHits, g.hits...)
			continue
		}
		if cached, ok := s.bundles.lookup(pkgKey); ok {
			bundles = append(bundles, cached...)
			continue
		}
		for _, n := range g.nodes {
			missIDs = append(missIDs, n.ID)
		}
		missNodes = append(missNodes, g.nodes...)
		missNodeHits = append(missNodeHits, g.hits...)
	}

	// Fetch edges for cache misses in a single batched round-trip.
	if len(missIDs) > 0 {
		inEdges := s.GetInEdgesByNodeIDs(missIDs)
		outEdges := s.GetOutEdgesByNodeIDs(missIDs)

		// Build hit→bundle lookup for cache-miss nodes.
		hitScore := map[string]float64{}
		for _, h := range missNodeHits {
			hitScore[h.NodeID] = h.Score
		}

		type freshEntry struct {
			bundle graph.SymbolBundle
			pkgKey string
		}
		var freshBundles []freshEntry
		for _, n := range missNodes {
			b := graph.SymbolBundle{
				Node:     n,
				Score:    hitScore[n.ID],
				InEdges:  inEdges[n.ID],
				OutEdges: outEdges[n.ID],
			}
			freshBundles = append(freshBundles, freshEntry{bundle: b, pkgKey: bundlePackageKey(n.FilePath)})
			bundles = append(bundles, b)
		}

		// Store per-package cache entries.
		if s.bundles != nil {
			pkgBundles := map[string][]graph.SymbolBundle{}
			for _, fb := range freshBundles {
				pkgBundles[fb.pkgKey] = append(pkgBundles[fb.pkgKey], fb.bundle)
			}
			for pk, pbs := range pkgBundles {
				s.bundles.store(pk, pbs)
			}
		}
	}

	return bundles, nil
}

// SetBundleFingerprints installs the authoritative per-package
// fingerprint map and drops any cached entry whose package fingerprint
// has changed (or whose package is no longer reported). This is the
// invalidation entry point: the daemon calls it after each analysis
// pass with the fresh fingerprints derived from the live graph.
func (s *Store) SetBundleFingerprints(fps map[string]uint64) {
	if s.bundles == nil {
		return
	}
	s.bundles.refresh(fps)
}

// BulkUpsertSymbolFTS replaces all symbol rows for a repo prefix.
// Since symbol search is on nodes.name directly, this is a no-op —
// the node table always has the current state.
func (s *Store) BulkUpsertSymbolFTS(repoPrefix string, items []graph.SymbolFTSItem) error {
	return nil
}

