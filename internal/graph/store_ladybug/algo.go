package store_ladybug

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	lbug "github.com/LadybugDB/go-ladybug"

	"github.com/zzet/gortex/internal/graph"
)

// algoProjectionName is the canonical name of the projected
// subgraph every algo CALL runs against. The projection is built
// once on demand and cached across algo invocations — withProjection
// only rebuilds when the cache key (node/edge filter) changes or
// the underlying graph mutates (Store.writeGen advanced). On
// gortex-scale graphs (313k+ edges) one PROJECT_GRAPH costs 30+s,
// so reusing it across consecutive algo runs is the difference
// between a 1.3 s analyze and a 63 s one.
const algoProjectionName = "GortexAlgo"

// projectionCacheEntry remembers the last successful PROJECT_GRAPH
// declaration so a repeat algo call with the same filter can skip
// the rebuild. generation is Store.writeGen at the time the
// projection was built; a mismatch with the current writeGen means
// the underlying graph has mutated and the projection is stale.
type projectionCacheEntry struct {
	valid      bool
	key        string // canonicalised projectionOpts (nodeKinds + edgeKinds)
	name       string // active projection name (currently always algoProjectionName)
	generation uint64 // Store.writeGen value when projection was built
}

// algoState tracks the per-store algo-extension lifecycle and
// the cached PROJECT_GRAPH declaration. The extension-load
// sentinel is durable; the projection is rebuilt lazily on the
// first algo call that follows a graph mutation (writeGen change)
// or a different filter shape.
type algoState struct {
	extensionLoaded atomic.Bool
	projectionMu    sync.Mutex // serialises projection-name use + cache mutation
	projection      projectionCacheEntry
}

// ensureAlgoExtensionLocked loads the ALGO extension into the
// active connection. Same dance as ensureVectorExtensionLocked /
// ensureFTSExtensionLocked (INSTALL + LOAD EXTENSION); idempotent
// via the sentinel. Held under writeMu by the caller.
//
// INSTALL / LOAD run on the setup conn (the same connection every
// later projection-lifecycle and algo CALL goes through). Routing
// the entire ALGO path to s.conn is required: Ladybug binds
// projected-graph declarations to the *connection* that ran
// PROJECT_GRAPH — a pooled connection sees no projection from
// a sibling pool slot, surfacing as "Projected graph G does not
// exists" the moment the algo CALL lands on a different pool conn.
func (s *Store) ensureAlgoExtensionLocked() error {
	if s.algo.extensionLoaded.Load() {
		return nil
	}
	if err := runCypherOnSetupSafe(s, `INSTALL ALGO`); err != nil &&
		!strings.Contains(err.Error(), "is already installed") {
		// Soft-ignore the "already installed" path — re-runs on the
		// same on-disk store re-INSTALL and a benign duplicate
		// shouldn't abort startup.
		_ = err
	}
	if err := runCypherOnSetupSafe(s, `LOAD EXTENSION ALGO`); err != nil {
		return fmt.Errorf("load algo extension: %w", err)
	}
	s.algo.extensionLoaded.Store(true)
	return nil
}

// projectionPredicate builds the per-table predicate map that
// PROJECT_GRAPH accepts when the caller wants to scope the algo
// to a subset of node kinds / edge kinds. Returns the literal
// predicate string ("'n.kind = "function" OR n.kind = "method"'")
// for substitution into the Cypher; an empty predicate falls
// through to the unfiltered list-of-tables form.
//
// Ladybug rejects predicates that reference more than one table,
// so node and edge predicates are emitted independently.
func projectionPredicates(opts projectionOpts) (nodePred, edgePred string) {
	if len(opts.nodeKinds) > 0 {
		parts := make([]string, 0, len(opts.nodeKinds))
		for _, k := range opts.nodeKinds {
			parts = append(parts, fmt.Sprintf(`n.kind = %q`, string(k)))
		}
		nodePred = strings.Join(parts, " OR ")
	}
	if len(opts.edgeKinds) > 0 {
		parts := make([]string, 0, len(opts.edgeKinds))
		for _, k := range opts.edgeKinds {
			parts = append(parts, fmt.Sprintf(`r.kind = %q`, string(k)))
		}
		edgePred = strings.Join(parts, " OR ")
	}
	return nodePred, edgePred
}

// projectionOpts is the union of every algo's per-call scoping
// knobs that map into PROJECT_GRAPH's filtered form. Each algo
// builds it from its public Opts struct.
type projectionOpts struct {
	nodeKinds []graph.NodeKind
	edgeKinds []graph.EdgeKind
}

// cacheKey returns a canonical serialisation of the projection
// shape — two opts with the same node/edge kinds (any order)
// produce the same key, so the cached projection is reused for
// repeat algo calls that differ only in their tuning knobs
// (dampingFactor, maxIterations, …). The key is intentionally
// cheap: a small string concat is dwarfed by the algo CALL itself.
func (o projectionOpts) cacheKey() string {
	// Sort for order-independence — callers may pass kinds in any
	// order, and the projection itself is order-insensitive.
	nodes := make([]string, len(o.nodeKinds))
	for i, k := range o.nodeKinds {
		nodes[i] = string(k)
	}
	edges := make([]string, len(o.edgeKinds))
	for i, k := range o.edgeKinds {
		edges[i] = string(k)
	}
	sortStrings(nodes)
	sortStrings(edges)
	return strings.Join(nodes, ",") + "|" + strings.Join(edges, ",")
}

// sortStrings is a tiny insertion sort over a string slice —
// fine for the handful of node/edge kinds an algo opts struct
// ever carries; pulls no stdlib sort import in.
func sortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		j := i
		for j > 0 && xs[j-1] > xs[j] {
			xs[j-1], xs[j] = xs[j], xs[j-1]
			j--
		}
	}
}

// projectGraphLocked declares the named projection. If predicates
// are non-empty, the filtered form (map-of-table-to-predicate) is
// used; otherwise the simple list form. Caller must already hold
// writeMu and the algo.projectionMu (acquired by withProjection).
func (s *Store) projectGraphLocked(name string, opts projectionOpts) error {
	nodePred, edgePred := projectionPredicates(opts)
	var q string
	switch {
	case nodePred == "" && edgePred == "":
		q = fmt.Sprintf(`CALL PROJECT_GRAPH('%s', ['Node'], ['Edge'])`, name)
	default:
		nodeArg := `['Node']`
		if nodePred != "" {
			nodeArg = fmt.Sprintf(`{'Node': '%s'}`, escapeCypherStringLit(nodePred))
		}
		edgeArg := `['Edge']`
		if edgePred != "" {
			edgeArg = fmt.Sprintf(`{'Edge': '%s'}`, escapeCypherStringLit(edgePred))
		}
		q = fmt.Sprintf(`CALL PROJECT_GRAPH('%s', %s, %s)`, name, nodeArg, edgeArg)
	}
	if err := runCypherOnSetupSafe(s, q); err != nil {
		return fmt.Errorf("project graph %q: %w", name, err)
	}
	return nil
}

// dropProjectionLocked tears down the named projection. Logs but
// does not propagate errors — a stale projection from a crashed
// run shouldn't block the next algo call. Pinned to the setup
// conn (same conn as projectGraphLocked) so the drop targets the
// right per-connection catalog.
func (s *Store) dropProjectionLocked(name string) {
	_ = runCypherOnSetupSafe(s, fmt.Sprintf(`CALL DROP_PROJECTED_GRAPH('%s')`, name))
}

// withProjection wraps an algo CALL in the project → run lifecycle
// with a projection cache. The first call for a given (nodeKinds,
// edgeKinds) shape declares the projection; subsequent calls with
// the same shape and an unchanged Store.writeGen reuse it — no
// CALL PROJECT_GRAPH, no CALL DROP_PROJECTED_GRAPH. The cache is
// invalidated lazily: a mismatch between the cached generation and
// the live writeGen triggers a drop+rebuild on the next call.
//
// The algo.projectionMu mutex serialises projection-name reuse +
// cache mutation across concurrent algo invocations. writeMu is
// taken inside it so an unrelated write can't slip in between the
// generation read and the projection rebuild (which would race the
// cache into an apparently-fresh-but-actually-stale state).
//
// Why no drop after fn: the algo CALL is a read-only query against
// the projection — leaving the projection live across calls turns
// the second-and-later PageRank / Louvain / WCC / SCC / KCore call
// into a pure algorithm run instead of a full graph rebuild. On
// gortex-scale graphs (313k+ edges) that's the difference between
// ~1 s and ~30 s per call.
func (s *Store) withProjection(opts projectionOpts, fn func(name string) error) error {
	s.algo.projectionMu.Lock()
	defer s.algo.projectionMu.Unlock()

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := s.ensureAlgoExtensionLocked(); err != nil {
		return err
	}

	key := opts.cacheKey()
	gen := s.writeGen.Load()

	// Fast path: cached projection still matches the requested
	// shape AND the graph hasn't mutated since it was built.
	if s.algo.projection.valid &&
		s.algo.projection.key == key &&
		s.algo.projection.generation == gen {
		return fn(s.algo.projection.name)
	}

	// Cache miss (different shape, stale generation, or first
	// call). Drop the previous projection if one is live, then
	// rebuild against the requested opts. The cache stays invalid
	// across the rebuild so a PROJECT_GRAPH failure leaves us in
	// a clean "no projection" state for the next call to retry.
	if s.algo.projection.valid {
		s.dropProjectionLocked(s.algo.projection.name)
		s.algo.projection.valid = false
	}
	// Defensive drop for a stale projection from a prior crashed
	// run (or a previous Open of the same on-disk store) that
	// would otherwise make PROJECT_GRAPH fail with "graph G
	// already exists".
	s.dropProjectionLocked(algoProjectionName)

	if err := s.projectGraphLocked(algoProjectionName, opts); err != nil {
		return err
	}
	s.algo.projection = projectionCacheEntry{
		valid:      true,
		key:        key,
		name:       algoProjectionName,
		generation: gen,
	}
	return fn(algoProjectionName)
}

// dropCachedProjection tears down any cached projection. Called
// from Store.Close so the engine's catalog doesn't carry a
// dangling projection across the connection teardown.
func (s *Store) dropCachedProjection() {
	s.algo.projectionMu.Lock()
	defer s.algo.projectionMu.Unlock()
	if !s.algo.projection.valid {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.dropProjectionLocked(s.algo.projection.name)
	s.algo.projection.valid = false
}

// runCypherOnSetupSafe is runCypherSafe but pinned to the setup
// connection (s.conn) instead of round-tripping through the pool.
// The ALGO extension's CALL PROJECT_GRAPH binds the projection to
// the connection that ran it — every later CALL <algo> from a
// different pool connection would surface "Projected graph G
// does not exists". Pinning the entire projection lifecycle
// (INSTALL + LOAD + PROJECT_GRAPH + CALL <algo> + DROP) to s.conn
// guarantees per-connection consistency.
func runCypherOnSetupSafe(s *Store, query string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				err = e
				return
			}
			err = fmt.Errorf("%v", r)
		}
	}()
	if s.conn == nil {
		// Test fixtures may construct a Store{} without Open — fall
		// back to the regular pool-aware path.
		s.runWriteLocked(query, nil)
		return nil
	}
	res, qerr := s.conn.Query(query)
	if qerr != nil {
		return qerr
	}
	res.Close()
	return nil
}

// querySelectOnSetupSafe is querySelectSafe pinned to the setup
// connection — same rationale as runCypherOnSetupSafe.
func querySelectOnSetupSafe(s *Store, query string, args map[string]any) (rows [][]any, err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				err = e
				return
			}
			err = fmt.Errorf("%v", r)
		}
	}()
	if s.conn == nil {
		// Test fixtures may construct a Store{} without Open — fall
		// back to the regular pool-aware path.
		rows = s.querySelectLocked(query, args)
		return rows, nil
	}
	var res *lbug.QueryResult
	if len(args) == 0 {
		res, err = s.conn.Query(query)
		if err != nil {
			return nil, err
		}
	} else {
		stmt, perr := s.conn.Prepare(query)
		if perr != nil {
			return nil, fmt.Errorf("prepare: %w", perr)
		}
		defer stmt.Close()
		res, err = s.conn.Execute(stmt, args)
		if err != nil {
			return nil, err
		}
	}
	defer res.Close()
	for res.HasNext() {
		tup, terr := res.Next()
		if terr != nil {
			return rows, terr
		}
		vals, verr := tup.GetAsSlice()
		if verr != nil {
			tup.Close()
			return rows, verr
		}
		rows = append(rows, vals)
		tup.Close()
	}
	return rows, nil
}

// PageRank computes PageRank centrality over a projected subgraph.
// Returns hits sorted by rank descending; the rank values sum to ~1
// across the projection (Ladybug normalises initial scores by
// default).
//
// Zero-valued opts map to the backend's default tuning. The
// projection name and lifetime are managed internally — callers
// don't touch CALL PROJECT_GRAPH directly.
func (s *Store) PageRank(opts graph.PageRankOpts) ([]graph.PageRankHit, error) {
	projOpts := projectionOpts{nodeKinds: opts.NodeKinds, edgeKinds: opts.EdgeKinds}

	// Build the page_rank CALL with only the overridden tuning
	// knobs as named args. Leaving a knob out delegates to
	// Ladybug's parallel-tuned defaults (dampingFactor=0.85,
	// maxIterations=20, tolerance=1e-7).
	var args []string
	if opts.DampingFactor > 0 {
		args = append(args, fmt.Sprintf("dampingFactor := %g", opts.DampingFactor))
	}
	if opts.MaxIterations > 0 {
		args = append(args, fmt.Sprintf("maxIterations := %d", opts.MaxIterations))
	}
	if opts.Tolerance > 0 {
		args = append(args, fmt.Sprintf("tolerance := %g", opts.Tolerance))
	}
	knobs := ""
	if len(args) > 0 {
		knobs = ", " + strings.Join(args, ", ")
	}

	limitClause := ""
	if opts.Limit > 0 {
		limitClause = fmt.Sprintf(" LIMIT %d", opts.Limit)
	}

	var hits []graph.PageRankHit
	err := s.withProjection(projOpts, func(name string) error {
		q := fmt.Sprintf(
			`CALL page_rank('%s'%s) RETURN node.id AS id, rank ORDER BY rank DESC%s`,
			name, knobs, limitClause,
		)
		rows, err := querySelectOnSetupSafe(s, q, nil)
		if err != nil {
			return fmt.Errorf("page_rank: %w", err)
		}
		hits = make([]graph.PageRankHit, 0, len(rows))
		for _, row := range rows {
			if len(row) < 2 {
				continue
			}
			id, _ := row[0].(string)
			if id == "" {
				continue
			}
			rank, _ := row[1].(float64)
			hits = append(hits, graph.PageRankHit{NodeID: id, Rank: rank})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return hits, nil
}

// Louvain runs community detection over a projected subgraph and
// returns one hit per node with the integer community label the
// algorithm assigned. Ladybug treats edges as undirected when
// computing modularity even though the projected Edge table is
// directed — callers that care about directed modularity should
// run the in-process fallback (analysis.DetectCommunitiesLouvain).
//
// CommunityID values are opaque integers (Ladybug uses internal
// node offsets); two nodes with the same ID are in the same
// community, but the integer itself isn't stable across runs.
func (s *Store) Louvain(opts graph.CommunityOpts) ([]graph.CommunityHit, error) {
	projOpts := projectionOpts{nodeKinds: opts.NodeKinds, edgeKinds: opts.EdgeKinds}

	var args []string
	if opts.MaxPhases > 0 {
		args = append(args, fmt.Sprintf("maxPhases := %d", opts.MaxPhases))
	}
	if opts.MaxIterations > 0 {
		args = append(args, fmt.Sprintf("maxIterations := %d", opts.MaxIterations))
	}
	knobs := ""
	if len(args) > 0 {
		knobs = ", " + strings.Join(args, ", ")
	}

	var hits []graph.CommunityHit
	err := s.withProjection(projOpts, func(name string) error {
		q := fmt.Sprintf(
			`CALL louvain('%s'%s) RETURN node.id AS id, louvain_id`,
			name, knobs,
		)
		rows, err := querySelectOnSetupSafe(s, q, nil)
		if err != nil {
			return fmt.Errorf("louvain: %w", err)
		}
		hits = make([]graph.CommunityHit, 0, len(rows))
		for _, row := range rows {
			if len(row) < 2 {
				continue
			}
			id, _ := row[0].(string)
			if id == "" {
				continue
			}
			cid := asInt64(row[1])
			hits = append(hits, graph.CommunityHit{NodeID: id, CommunityID: cid})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return hits, nil
}

// WeaklyConnectedComponents runs WCC (undirected reachability)
// over a projected subgraph. Returns one hit per node with the
// integer component label; two nodes with the same ComponentID
// are in the same WCC.
func (s *Store) WeaklyConnectedComponents(opts graph.ComponentOpts) ([]graph.ComponentHit, error) {
	return s.runComponentAlgo("weakly_connected_components", opts)
}

// StronglyConnectedComponents runs SCC (directional mutual
// reachability) over a projected subgraph. Two nodes share an
// SCC iff they are mutually reachable along directed edges; SCCs
// of size > 1 are the cycle structure of the directed graph.
//
// Ladybug ships two SCC implementations — a BFS-based default
// (used here) and a Kosaraju DFS variant
// (strongly_connected_components_kosaraju) "recommended for sparse
// graphs or those with high diameter" per the docs. Callers that
// need Kosaraju behaviour can invoke graph_query directly.
func (s *Store) StronglyConnectedComponents(opts graph.ComponentOpts) ([]graph.ComponentHit, error) {
	return s.runComponentAlgo("strongly_connected_components", opts)
}

// KCoreDecomposition runs the k-core decomposition over a
// projected subgraph and returns one hit per node carrying its
// k-degree — the largest k for which the node stays in the
// k-core after iterative degree-< k pruning.
//
// Ladybug's CALL k_core_decomposition takes no tuning knobs
// (the algorithm always computes the full decomposition); the
// only per-call shaping comes from PROJECT_GRAPH's NodeKinds /
// EdgeKinds filter.
func (s *Store) KCoreDecomposition(opts graph.KCoreOpts) ([]graph.KCoreHit, error) {
	projOpts := projectionOpts{nodeKinds: opts.NodeKinds, edgeKinds: opts.EdgeKinds}

	var hits []graph.KCoreHit
	err := s.withProjection(projOpts, func(name string) error {
		q := fmt.Sprintf(
			`CALL k_core_decomposition('%s') RETURN node.id AS id, k_degree`,
			name,
		)
		rows, err := querySelectOnSetupSafe(s, q, nil)
		if err != nil {
			return fmt.Errorf("k_core_decomposition: %w", err)
		}
		hits = make([]graph.KCoreHit, 0, len(rows))
		for _, row := range rows {
			if len(row) < 2 {
				continue
			}
			id, _ := row[0].(string)
			if id == "" {
				continue
			}
			hits = append(hits, graph.KCoreHit{NodeID: id, KDegree: asInt64(row[1])})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return hits, nil
}

// runComponentAlgo is the shared shape for the two component
// algos. cypherCall is the algo's CALL name; both algos return
// the same (node, group_id) shape.
func (s *Store) runComponentAlgo(cypherCall string, opts graph.ComponentOpts) ([]graph.ComponentHit, error) {
	projOpts := projectionOpts{nodeKinds: opts.NodeKinds, edgeKinds: opts.EdgeKinds}

	knobs := ""
	if opts.MaxIterations > 0 {
		knobs = fmt.Sprintf(", maxIterations := %d", opts.MaxIterations)
	}

	var hits []graph.ComponentHit
	err := s.withProjection(projOpts, func(name string) error {
		q := fmt.Sprintf(
			`CALL %s('%s'%s) RETURN node.id AS id, group_id`,
			cypherCall, name, knobs,
		)
		rows, err := querySelectOnSetupSafe(s, q, nil)
		if err != nil {
			return fmt.Errorf("%s: %w", cypherCall, err)
		}
		hits = make([]graph.ComponentHit, 0, len(rows))
		for _, row := range rows {
			if len(row) < 2 {
				continue
			}
			id, _ := row[0].(string)
			if id == "" {
				continue
			}
			hits = append(hits, graph.ComponentHit{NodeID: id, ComponentID: asInt64(row[1])})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return hits, nil
}
