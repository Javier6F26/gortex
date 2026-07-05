package store_pg

import (
	"fmt"
	"sort"
	"strings"

	"github.com/zzet/gortex/internal/graph"
)

// This file implements graph.BFSCapable on the PostgreSQL backend using a
// recursive CTE — the disk-backed sibling of the in-memory BFS walk and the
// SQLite recursive CTE. PostgreSQL's WITH RECURSIVE provides the same
// semantics as SQLite's, so this implementation mirrors store_sqlite/store_bfs.go
// closely.

// Compile-time assertion.
var _ graph.BFSCapable = (*Store)(nil)

// BFS runs a bounded breadth-first traversal in a single round-trip via a
// recursive CTE. See graph.BFSCapable for the full contract.
func (s *Store) BFS(seeds []string, dir graph.Direction, kinds []graph.EdgeKind, maxDepth, limit int) ([]graph.BFSHop, error) {
	seen := make(map[string]struct{}, len(seeds))
	uniqSeeds := make([]string, 0, len(seeds))
	for _, sd := range seeds {
		if sd == "" {
			continue
		}
		if _, ok := seen[sd]; ok {
			continue
		}
		seen[sd] = struct{}{}
		uniqSeeds = append(uniqSeeds, sd)
	}
	if len(uniqSeeds) == 0 {
		return nil, nil
	}

	uniqKinds := dedupeEdgeKinds(kinds)

	// Seed-only fast path.
	if len(uniqKinds) == 0 || maxDepth <= 0 {
		hops := make([]graph.BFSHop, 0, len(uniqSeeds))
		for _, sd := range uniqSeeds {
			hops = append(hops, graph.BFSHop{NodeID: sd, Depth: 0})
		}
		sortBFSHops(hops)
		if limit > 0 && len(hops) > limit {
			hops = hops[:limit]
		}
		return hops, nil
	}

	// Direction-specific join column.
	joinCol := "to_id"   // forward: follow outgoing edges (from -> to)
	whereCol := "from_id" // incoming edges from the seed
	if dir == graph.DirectionBackward {
		joinCol = "from_id"  // backward: follow incoming edges (to -> from)
		whereCol = "to_id"
	}

	kindList := make([]string, len(uniqKinds))
	for i, k := range uniqKinds {
		kindList[i] = fmt.Sprintf("'%s'", escapeSQLString(string(k)))
	}
	kindClause := strings.Join(kindList, ",")

	// Build the recursive CTE.
	// The anchor selects seeds; the recursive term joins edges and deduplicates
	// by node_id, keeping only the minimum-depth entry per node via ROW_NUMBER.
	cte := fmt.Sprintf(`
	WITH RECURSIVE bfs AS (
		SELECT n.id AS node_id, 0 AS depth, ''::TEXT AS parent_id, ''::TEXT AS edge_kind
		FROM nodes n
		WHERE n.id = ANY($1)
		UNION
		SELECT e.%s AS node_id, b.depth + 1, b.node_id, e.kind
		FROM bfs b
		JOIN edges e ON e.%s = b.node_id AND e.kind IN (%s)
		JOIN nodes n ON n.id = e.%s
		WHERE b.depth < $2
	),
	ranked AS (
		SELECT node_id, depth, parent_id, edge_kind,
			ROW_NUMBER() OVER (PARTITION BY node_id ORDER BY depth, parent_id, edge_kind) AS rn
		FROM bfs
	)
	SELECT node_id, depth, parent_id, edge_kind
	FROM ranked
	WHERE rn = 1
	ORDER BY depth, node_id
	`, joinCol, whereCol, kindClause, joinCol)

	var limitClause string
	if limit > 0 {
		limitClause = fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := s.pool.Query(s.ctx, cte+limitClause, uniqSeeds, maxDepth)
	if err != nil {
		return nil, fmt.Errorf("store_pg: BFS: %w", err)
	}
	defer rows.Close()

	var hops []graph.BFSHop
	for rows.Next() {
		var h graph.BFSHop
		var depth int
		if err := rows.Scan(&h.NodeID, &depth, &h.ParentID, &h.EdgeKind); err != nil {
			return hops, err
		}
		h.Depth = depth
		hops = append(hops, h)
	}
	return hops, rows.Err()
}

func dedupeEdgeKinds(kinds []graph.EdgeKind) []graph.EdgeKind {
	seen := make(map[graph.EdgeKind]struct{}, len(kinds))
	out := make([]graph.EdgeKind, 0, len(kinds))
	for _, k := range kinds {
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

func sortBFSHops(hops []graph.BFSHop) {
	sort.Slice(hops, func(i, j int) bool {
		if hops[i].Depth != hops[j].Depth {
			return hops[i].Depth < hops[j].Depth
		}
		return hops[i].NodeID < hops[j].NodeID
	})
}

func escapeSQLString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
