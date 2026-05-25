package store_ladybug

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/zzet/gortex/internal/graph"
)

// algoProjectionName is the canonical name of the projected
// subgraph every algo CALL runs against. Bound per call: we
// declare → run → drop in one writeMu-held sequence so a
// concurrent algo never races against a stale projection's name.
const algoProjectionName = "GortexAlgo"

// algoState tracks the per-store algo-extension lifecycle. Only
// the extension-load sentinel is durable; the projection is
// per-call and lives only inside the writeMu-held critical
// section that wraps a single algo invocation.
type algoState struct {
	extensionLoaded atomic.Bool
	projectionMu    sync.Mutex // serialises PROJECT_GRAPH name reuse
}

// ensureAlgoExtensionLocked loads the ALGO extension into the
// active connection. Same dance as ensureVectorExtensionLocked /
// ensureFTSExtensionLocked (INSTALL + LOAD EXTENSION); idempotent
// via the sentinel. Held under writeMu by the caller.
func (s *Store) ensureAlgoExtensionLocked() error {
	if s.algo.extensionLoaded.Load() {
		return nil
	}
	if err := runCypherSafe(s, `INSTALL ALGO`); err != nil &&
		!strings.Contains(err.Error(), "is already installed") {
		// Soft-ignore the "already installed" path — re-runs on the
		// same on-disk store re-INSTALL and a benign duplicate
		// shouldn't abort startup.
		_ = err
	}
	if err := runCypherSafe(s, `LOAD EXTENSION ALGO`); err != nil {
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
	if err := runCypherSafe(s, q); err != nil {
		return fmt.Errorf("project graph %q: %w", name, err)
	}
	return nil
}

// dropProjectionLocked tears down the named projection. Logs but
// does not propagate errors — a stale projection from a crashed
// run shouldn't block the next algo call.
func (s *Store) dropProjectionLocked(name string) {
	_ = runCypherSafe(s, fmt.Sprintf(`CALL DROP_PROJECTED_GRAPH('%s')`, name))
}

// withProjection wraps an algo CALL in the project → run → drop
// lifecycle. The caller passes a function that consumes the
// projection name and runs whatever Cypher it needs; the helper
// acquires writeMu, loads the extension, declares the projection,
// invokes the callback, and drops the projection on the way out
// (including on error paths).
//
// The algo.projectionMu mutex serialises projection-name reuse
// across concurrent algo invocations on the same store —
// PROJECT_GRAPH errors out if the name is already in use.
func (s *Store) withProjection(opts projectionOpts, fn func(name string) error) error {
	s.algo.projectionMu.Lock()
	defer s.algo.projectionMu.Unlock()

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := s.ensureAlgoExtensionLocked(); err != nil {
		return err
	}
	// Defensive drop in case a prior call crashed mid-flight.
	s.dropProjectionLocked(algoProjectionName)
	if err := s.projectGraphLocked(algoProjectionName, opts); err != nil {
		return err
	}
	defer s.dropProjectionLocked(algoProjectionName)
	return fn(algoProjectionName)
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
		rows, err := querySelectSafe(s, q, nil)
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
		rows, err := querySelectSafe(s, q, nil)
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

