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
func DetectCrossRepoEdges(g *graph.Graph) int {
	if g == nil {
		return 0
	}
	emitted := 0
	for _, e := range g.AllEdges() {
		if e == nil {
			continue
		}
		crKind, ok := graph.CrossRepoKindFor(e.Kind)
		if !ok {
			continue
		}
		from := g.GetNode(e.From)
		to := g.GetNode(e.To)
		if from == nil || to == nil {
			// Unresolved / external / stdlib / dep stub targets never
			// have a graph node — they cannot be cross-repo.
			continue
		}
		if from.RepoPrefix == "" || to.RepoPrefix == "" {
			// Single-repo graph (no prefixes) — nothing crosses a
			// boundary. Also covers a node whose repo wasn't stamped.
			continue
		}
		if from.RepoPrefix == to.RepoPrefix {
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
				"source_repo": from.RepoPrefix,
				"target_repo": to.RepoPrefix,
			},
		})
		emitted++
	}
	return emitted
}
