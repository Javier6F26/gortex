package resolver

import "github.com/zzet/gortex/internal/graph"

// DetectCrossRepoEdges is the graph-wide materialisation pass for the
// cross-repo edge layer (M3). It walks every resolved calls / implements
// / extends edge and, whenever the From node and the To node live in
// two different repos, emits a parallel edge of the matching
// cross_repo_* kind and sets Edge.CrossRepo on the base edge so the
// bool flag and the dedicated kind never disagree.
//
// The pass is a full recompute and is idempotent: graph.AddEdge dedupes
// by edgeKey, so re-emitting an unchanged parallel edge is a no-op. It
// is also incremental-safe — graph.EvictFile removes a node's edges in
// both directions, so when either endpoint's file is reindexed the
// stale parallel edge is gone before this pass re-runs. Parallel
// cross_repo_* edges are themselves skipped (CrossRepoKindFor only maps
// the three base kinds), so the pass never feeds on its own output.
//
// Runs at every resolver "settle" point: the tail of
// CrossRepoResolver.ResolveAll / ResolveForRepo (cross-repo calls just
// lifted by the boundary resolver) and inside the indexers'
// RunGlobalGraphPasses (cross-repo implements / extends just produced
// by InferImplements / InferOverrides).
//
// Returns the count of cross-repo relationships found this pass — the
// number of parallel edges that exist after it, modulo graph dedup.
func DetectCrossRepoEdges(g graph.Store) int {
	if g == nil {
		return 0
	}
	emitted := 0
	for _, row := range crossRepoCandidates(g) {
		e := row.Edge
		if e == nil {
			continue
		}
		crKind, ok := graph.CrossRepoKindFor(e.Kind)
		if !ok {
			continue
		}
		// Keep the bool flag on the base edge consistent with the
		// dedicated kind — existing consumers (smart_context's
		// cross_repo_dependencies, the Cypher / GraphML exporters) read
		// Edge.CrossRepo, and structurally-resolved cross-repo edges
		// would otherwise carry the parallel kind without the flag.
		e.CrossRepo = true
		g.AddEdge(&graph.Edge{
			From:            e.From,
			To:              e.To,
			Kind:            crKind,
			FilePath:        e.FilePath,
			Line:            e.Line,
			Confidence:      e.Confidence,
			ConfidenceLabel: e.ConfidenceLabel,
			Origin:          e.Origin,
			CrossRepo:       true,
			Meta: map[string]any{
				"base_kind":   string(e.Kind),
				"source_repo": row.FromRepo,
				"target_repo": row.ToRepo,
			},
		})
		emitted++
	}
	return emitted
}

// crossRepoCandidates returns every edge whose Kind has a parallel
// cross_repo_* kind AND whose endpoints carry two distinct, non-empty
// RepoPrefix values. Routed through the storage layer's
// CrossRepoCandidates capability when the backend implements it (one
// Cypher join with the kind + repo-prefix filters in WHERE); falls
// back to the AllEdges + per-edge GetNode walk otherwise.
//
// The base-kind set is derived from graph.CrossRepoKindFor by
// iterating the in-process registry — the disk backend uses the same
// kind list verbatim so single-repo graphs return no rows without a
// whole-table scan.
func crossRepoCandidates(g graph.Store) []graph.CrossRepoCandidateRow {
	baseKinds := graph.BaseKindsForCrossRepo()
	if cap, ok := g.(graph.CrossRepoCandidates); ok {
		return cap.CrossRepoCandidates(baseKinds)
	}
	if len(baseKinds) == 0 {
		return nil
	}
	kset := make(map[graph.EdgeKind]struct{}, len(baseKinds))
	for _, k := range baseKinds {
		kset[k] = struct{}{}
	}
	var out []graph.CrossRepoCandidateRow
	for _, e := range g.AllEdges() {
		if e == nil {
			continue
		}
		if _, ok := kset[e.Kind]; !ok {
			continue
		}
		from := g.GetNode(e.From)
		to := g.GetNode(e.To)
		if from == nil || to == nil {
			continue
		}
		if from.RepoPrefix == "" || to.RepoPrefix == "" {
			continue
		}
		if from.RepoPrefix == to.RepoPrefix {
			continue
		}
		out = append(out, graph.CrossRepoCandidateRow{
			Edge:     e,
			FromRepo: from.RepoPrefix,
			ToRepo:   to.RepoPrefix,
		})
	}
	return out
}
