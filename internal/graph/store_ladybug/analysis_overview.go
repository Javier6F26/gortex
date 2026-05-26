package store_ladybug

import (
	"github.com/zzet/gortex/internal/graph"
)

// Compile-time assertions: *Store satisfies the overview-aggregate
// capabilities so the get_repo_outline / get_architecture /
// get_surprising_connections / suggest_queries handlers pick the
// server-side path via type assertion. Signature drift fails the
// build here instead of silently falling back to the Go loop.
var (
	_ graph.EdgeKindCounter         = (*Store)(nil)
	_ graph.CrossRepoEdgeAggregator = (*Store)(nil)
	_ graph.FileImportAggregator    = (*Store)(nil)
)

// EdgeKindCounts runs the per-kind tally inside Ladybug. Replaces
// the AllEdges() bucket pass that get_surprising_connections used to
// derive its "rare kinds" set — on the gortex workspace that pulled
// ~286k edge rows over cgo just to bucket ~30 distinct kinds. The
// Cypher GROUP BY ships back one row per kind: typically a handful
// across the entire repo.
func (s *Store) EdgeKindCounts() map[graph.EdgeKind]int {
	const q = `
MATCH ()-[e:Edge]->()
RETURN e.kind, count(*)`
	rows := s.querySelect(q, nil)
	if len(rows) == 0 {
		return nil
	}
	out := make(map[graph.EdgeKind]int, len(rows))
	for _, r := range rows {
		if len(r) < 2 {
			continue
		}
		kind, _ := r[0].(string)
		if kind == "" {
			continue
		}
		out[graph.EdgeKind(kind)] = int(asInt64(r[1]))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// CrossRepoEdgeCounts runs the (kind, fromRepo, toRepo) rollup
// inside Ladybug. Replaces the AllEdges() + per-edge GetNode pair
// in handleGetArchitecture — on the gortex workspace that loop
// materialised every edge over cgo plus thousands of per-edge
// GetNode round-trips to emit typically <100 cross-repo rows. One
// Cypher join now ships only the surviving per-triple counts.
//
// The IN list mirrors graph.BaseKindForCrossRepo (the canonical
// cross-repo edge-kind set) — a fresh kind landing in
// internal/graph/edge.go without a corresponding update here would
// quietly drop from the rollup, so the kind list is duplicated by
// design (one-place change still tractable) rather than reflected
// at runtime.
func (s *Store) CrossRepoEdgeCounts() []graph.CrossRepoEdgeRow {
	const q = `
MATCH (from:Node)-[e:Edge]->(to:Node)
WHERE e.kind IN $kinds
RETURN e.kind, from.repo_prefix, to.repo_prefix, count(*)`
	args := map[string]any{
		"kinds": []any{
			string(graph.EdgeCrossRepoCalls),
			string(graph.EdgeCrossRepoImplements),
			string(graph.EdgeCrossRepoExtends),
		},
	}
	rows := s.querySelect(q, args)
	if len(rows) == 0 {
		return nil
	}
	out := make([]graph.CrossRepoEdgeRow, 0, len(rows))
	for _, r := range rows {
		if len(r) < 4 {
			continue
		}
		kind, _ := r[0].(string)
		if kind == "" {
			continue
		}
		fromRepo, _ := r[1].(string)
		toRepo, _ := r[2].(string)
		out = append(out, graph.CrossRepoEdgeRow{
			Kind:     graph.EdgeKind(kind),
			FromRepo: fromRepo,
			ToRepo:   toRepo,
			Count:    int(asInt64(r[3])),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// FileImportCounts runs the per-target-file import-count rollup
// inside Ladybug. Replaces the AllEdges() + per-edge GetNode loop
// in mostImportedFiles — that pass materialised every edge over
// cgo (~286k on the gortex workspace) plus a per-edge GetNode
// round-trip just to produce a top-10 list. The Cypher GROUP BY
// returns one row per imported file path.
//
// The COALESCE mirrors the indexer's two import shapes: file-
// targeted imports point at the file node (whose ID is the path),
// symbol-targeted imports land on a symbol whose FilePath holds
// the path. The Go-side ranker handles the top-N truncation and
// the file-path-vs-ID humanising — keep that out of Cypher.
//
// scope, when non-nil, bounds the counted edges to those whose
// target ID lies in the slice. An empty (non-nil) scope returns
// nil (mirroring the in-memory contract) — never a whole-graph
// scan. A nil scope counts every imports edge.
func (s *Store) FileImportCounts(scope []string) []graph.FileImportCountRow {
	if scope != nil && len(scope) == 0 {
		return nil
	}
	scopeArg := dedupeNonEmpty(scope)
	if scope != nil && len(scopeArg) == 0 {
		return nil
	}

	// COALESCE folds file-id-targeted vs symbol-FilePath-targeted
	// imports into a single grouping key. Without it the rollup
	// would split popular.go's count across "popular.go" and
	// "PopularFn".
	q := `
MATCH (from:Node)-[e:Edge]->(to:Node)
WHERE e.kind = $imp
  AND (to.file_path IS NOT NULL OR to.id IS NOT NULL)
RETURN coalesce(to.file_path, to.id), count(*)`
	args := map[string]any{"imp": string(graph.EdgeImports)}
	if scope != nil {
		q = `
MATCH (from:Node)-[e:Edge]->(to:Node)
WHERE e.kind = $imp
  AND to.id IN $scope
  AND (to.file_path IS NOT NULL OR to.id IS NOT NULL)
RETURN coalesce(to.file_path, to.id), count(*)`
		args["scope"] = stringSliceToAny(scopeArg)
	}
	rows := s.querySelect(q, args)
	if len(rows) == 0 {
		return nil
	}
	out := make([]graph.FileImportCountRow, 0, len(rows))
	for _, r := range rows {
		if len(r) < 2 {
			continue
		}
		path, _ := r[0].(string)
		if path == "" {
			continue
		}
		out = append(out, graph.FileImportCountRow{
			FilePath: path,
			Count:    int(asInt64(r[1])),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
