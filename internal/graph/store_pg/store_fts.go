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
	if s.refuseWrite("UpsertSymbolFTS") {
		return ErrReadOnlyStore
	}
	return nil
}

// BuildSymbolIndex ensures the pg_trgm GIN index on names and the
// doc-body tsvector GIN index both exist. Idempotent.
func (s *Store) BuildSymbolIndex() error {
	if s.refuseWrite("BuildSymbolIndex") {
		return ErrReadOnlyStore
	}
	if _, err := s.pool.Exec(s.ctx, `CREATE INDEX IF NOT EXISTS idx_nodes_name_trgm ON nodes USING GIN (name gin_trgm_ops)`); err != nil {
		return err
	}
	// Doc-section body full-text index: a partial GIN expression index over
	// the section_text stored in each KindDoc node's meta, so searchDocBodies'
	// `@@` lookup is served from the index rather than a sequential scan.
	_, err := s.pool.Exec(s.ctx,
		`CREATE INDEX IF NOT EXISTS idx_nodes_doc_body_fts ON nodes
		 USING GIN (to_tsvector('english', meta->>'section_text'))
		 WHERE kind = 'doc'`)
	return err
}

// SearchSymbols runs a pg_trgm similarity query and returns hits ordered
// by score descending. Tier 0: exact name matches short-circuit for simple
// identifier queries. In every tier the KindDoc prose-section body channel
// (searchDocBodies) is merged in AFTER the name hits so a query whose terms
// appear only in a section's body still returns that section — heading/name
// matches keep their lead (docs-corpus-search: match section body text).
func (s *Store) SearchSymbols(query string, limit int) ([]graph.SymbolHit, error) {
	if query == "" || limit <= 0 {
		return nil, nil
	}

	var nameHits []graph.SymbolHit
	// Tier 0: exact name match for identifier queries (no spaces/slashes).
	if !hasWhitespaceOrSlash(query) {
		exactHits, err := s.exactNameSearch(query, limit)
		if err != nil {
			return nil, err
		}
		nameHits = exactHits
	}
	// pg_trgm similarity search when no exact tier hit.
	if len(nameHits) == 0 {
		trgm, err := s.trgmSearch(query, limit)
		if err != nil {
			return nil, err
		}
		nameHits = trgm
	}

	// Doc-section body channel. The pre-tokenised symbol text (which on
	// SQLite carries the body into symbol_fts) is a no-op on pg — the
	// name column IS the trigram index — so body text is otherwise
	// invisible. Match it directly against the section_text stored in
	// meta so body-only queries resolve. Merged name-first so a code /
	// name query never has its hits crowded out by prose.
	docHits, err := s.searchDocBodies(query, limit)
	if err != nil {
		return nil, err
	}
	return mergeSymbolHitsNameFirst(nameHits, docHits, limit), nil
}

// searchDocBodies matches a query against the body text of KindDoc
// prose-section nodes, using a tsvector over the section_text stored in
// each node's meta. The GIN expression index built by BuildSymbolIndex
// keeps the `@@` lookup fast; scores are ts_rank, which the downstream
// rerank re-weighs. No backfill is needed — section_text is written by
// the markdown extractor into meta at index time, so existing stores are
// searchable as soon as the index exists.
func (s *Store) searchDocBodies(query string, limit int) ([]graph.SymbolHit, error) {
	rows, err := s.pool.Query(s.ctx,
		`SELECT id, ts_rank(to_tsvector('english', meta->>'section_text'), plainto_tsquery('english', $1)) AS score
		 FROM nodes
		 WHERE kind = 'doc'
		   AND meta->>'section_text' IS NOT NULL
		   AND to_tsvector('english', meta->>'section_text') @@ plainto_tsquery('english', $1)
		 ORDER BY score DESC
		 LIMIT $2`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("store_pg: doc body search: %w", err)
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

// mergeSymbolHitsNameFirst concatenates name-channel hits (in their
// score order) with body-channel hits, deduped by node ID, capped at
// limit. Name hits lead unconditionally so a heading / name match ranks
// at least as high as any body-only match and a code query's results are
// never displaced by prose — the two channels have incomparable raw
// scores (trigram similarity vs ts_rank), so ordering, not score, is the
// contract here.
func mergeSymbolHitsNameFirst(nameHits, docHits []graph.SymbolHit, limit int) []graph.SymbolHit {
	if len(docHits) == 0 {
		return nameHits
	}
	seen := make(map[string]struct{}, len(nameHits)+len(docHits))
	out := make([]graph.SymbolHit, 0, len(nameHits)+len(docHits))
	for _, h := range nameHits {
		if _, dup := seen[h.NodeID]; dup {
			continue
		}
		seen[h.NodeID] = struct{}{}
		out = append(out, h)
	}
	for _, h := range docHits {
		if len(out) >= limit {
			break
		}
		if _, dup := seen[h.NodeID]; dup {
			continue
		}
		seen[h.NodeID] = struct{}{}
		out = append(out, h)
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
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
