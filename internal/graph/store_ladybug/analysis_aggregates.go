package store_ladybug

import (
	"github.com/zzet/gortex/internal/graph"
)

// Compile-time assertions: *Store satisfies the per-node aggregate
// capabilities so the analyzers pick the server-side path via type
// assertion. A drift in either signature fails the build here instead
// of silently falling back to the Go loop.
var (
	_ graph.NodeDegreeAggregator = (*Store)(nil)
	_ graph.NodeFanAggregator    = (*Store)(nil)
	_ graph.EdgesByKindsScanner  = (*Store)(nil)
)

// NodeDegreeCounts evaluates per-node in/out/usage edge counts
// entirely inside Ladybug. Two Cypher queries: one for in-edges (and
// the usage subset), one for out-edges. The alternative — looping
// GetInEdges/GetOutEdges per node — fires 2N cgo round-trips and
// materialises every edge struct just to len() it. On the gortex
// workspace that loop fed GraphConnectivity ~133k nodes × 2 calls,
// each materialising the full edge bucket → ~95s wall and a sustained
// allocation spike. The aggregated path returns N compact rows in
// two queries.
//
// COUNT { ... } sub-queries return the bucket size without
// materialising the edges, which is what we actually want here.
func (s *Store) NodeDegreeCounts(ids []string, usageKinds []graph.EdgeKind) []graph.NodeDegreeRow {
	if len(ids) == 0 {
		return nil
	}
	uniq := dedupeNonEmpty(ids)
	if len(uniq) == 0 {
		return nil
	}
	usage := make([]any, 0, len(usageKinds))
	usageSeen := make(map[graph.EdgeKind]struct{}, len(usageKinds))
	for _, k := range usageKinds {
		if _, ok := usageSeen[k]; ok {
			continue
		}
		usageSeen[k] = struct{}{}
		usage = append(usage, string(k))
	}

	// One pass for in-counts (total + usage subset). Selecting both
	// in the same projection halves the cgo round-trips compared with
	// running the usage filter separately.
	inQuery := `
MATCH (n:Node)
WHERE n.id IN $ids
RETURN n.id,
       COUNT { MATCH (:Node)-[:Edge]->(n) },
       COUNT { MATCH (:Node)-[e:Edge]->(n) WHERE e.kind IN $usage }`
	if len(usage) == 0 {
		// No usage filter requested — drop the second COUNT to skip
		// the empty-IN-list edge case and shave a few µs from the
		// planner.
		inQuery = `
MATCH (n:Node)
WHERE n.id IN $ids
RETURN n.id,
       COUNT { MATCH (:Node)-[:Edge]->(n) },
       0`
	}
	inArgs := map[string]any{"ids": stringSliceToAny(uniq)}
	if len(usage) > 0 {
		inArgs["usage"] = usage
	}
	inRows := s.querySelect(inQuery, inArgs)

	const outQuery = `
MATCH (n:Node)
WHERE n.id IN $ids
RETURN n.id, COUNT { MATCH (n)-[:Edge]->(:Node) }`
	outRows := s.querySelect(outQuery, map[string]any{"ids": stringSliceToAny(uniq)})

	byID := make(map[string]*graph.NodeDegreeRow, len(uniq))
	for _, r := range inRows {
		if len(r) < 3 {
			continue
		}
		id, _ := r[0].(string)
		if id == "" {
			continue
		}
		byID[id] = &graph.NodeDegreeRow{
			NodeID:       id,
			InCount:      int(asInt64(r[1])),
			UsageInCount: int(asInt64(r[2])),
		}
	}
	for _, r := range outRows {
		if len(r) < 2 {
			continue
		}
		id, _ := r[0].(string)
		if id == "" {
			continue
		}
		row, ok := byID[id]
		if !ok {
			// Node had outgoing edges but no incoming (or vice
			// versa). Build the row from this pass so neither
			// direction is silently dropped.
			row = &graph.NodeDegreeRow{NodeID: id}
			byID[id] = row
		}
		row.OutCount = int(asInt64(r[1]))
	}

	out := make([]graph.NodeDegreeRow, 0, len(byID))
	for _, id := range uniq {
		if row, ok := byID[id]; ok {
			out = append(out, *row)
		}
	}
	return out
}

// NodeFanCounts evaluates per-node fan-in / fan-out counts filtered
// by edge kind entirely inside Ladybug. Two Cypher queries, one per
// direction. Replaces the AllEdges() scan that FindHotspots and
// handleAnalyzeHealthScore both ran every call — on the gortex
// workspace that was ~500k edge rows over cgo just to compute four
// integers per node.
//
// Empty fanInKinds / fanOutKinds short-circuits that direction's
// query — the Cypher planner does not love an empty IN-list and the
// caller already encoded "no fan" by passing nil.
func (s *Store) NodeFanCounts(ids []string, fanInKinds []graph.EdgeKind, fanOutKinds []graph.EdgeKind) []graph.NodeFanRow {
	if len(ids) == 0 {
		return nil
	}
	uniq := dedupeNonEmpty(ids)
	if len(uniq) == 0 {
		return nil
	}

	byID := make(map[string]*graph.NodeFanRow, len(uniq))
	ensure := func(id string) *graph.NodeFanRow {
		row, ok := byID[id]
		if !ok {
			row = &graph.NodeFanRow{NodeID: id}
			byID[id] = row
		}
		return row
	}

	if inKinds := dedupeEdgeKinds(fanInKinds); len(inKinds) > 0 {
		const q = `
MATCH (n:Node)
WHERE n.id IN $ids
RETURN n.id, COUNT { MATCH (:Node)-[e:Edge]->(n) WHERE e.kind IN $kinds }`
		rows := s.querySelect(q, map[string]any{
			"ids":   stringSliceToAny(uniq),
			"kinds": edgeKindSliceToAny(inKinds),
		})
		for _, r := range rows {
			if len(r) < 2 {
				continue
			}
			id, _ := r[0].(string)
			if id == "" {
				continue
			}
			ensure(id).FanIn = int(asInt64(r[1]))
		}
	}

	if outKinds := dedupeEdgeKinds(fanOutKinds); len(outKinds) > 0 {
		const q = `
MATCH (n:Node)
WHERE n.id IN $ids
RETURN n.id, COUNT { MATCH (n)-[e:Edge]->(:Node) WHERE e.kind IN $kinds }`
		rows := s.querySelect(q, map[string]any{
			"ids":   stringSliceToAny(uniq),
			"kinds": edgeKindSliceToAny(outKinds),
		})
		for _, r := range rows {
			if len(r) < 2 {
				continue
			}
			id, _ := r[0].(string)
			if id == "" {
				continue
			}
			ensure(id).FanOut = int(asInt64(r[1]))
		}
	}

	// When BOTH directions are filtered out, the caller asked for
	// nothing — return an empty row per known id rather than nil,
	// matching the in-memory reference's behaviour.
	if len(byID) == 0 {
		out := make([]graph.NodeFanRow, 0, len(uniq))
		for _, id := range uniq {
			out = append(out, graph.NodeFanRow{NodeID: id})
		}
		// Honour the contract that unknown ids are elided — when
		// neither direction matched ANY id, the result is empty.
		// Filter by membership in the node table.
		const probe = `MATCH (n:Node) WHERE n.id IN $ids RETURN n.id`
		seen := make(map[string]struct{}, len(uniq))
		for _, r := range s.querySelect(probe, map[string]any{"ids": stringSliceToAny(uniq)}) {
			if len(r) < 1 {
				continue
			}
			id, _ := r[0].(string)
			if id != "" {
				seen[id] = struct{}{}
			}
		}
		filtered := out[:0]
		for _, row := range out {
			if _, ok := seen[row.NodeID]; ok {
				filtered = append(filtered, row)
			}
		}
		return filtered
	}

	out := make([]graph.NodeFanRow, 0, len(byID))
	for _, id := range uniq {
		if row, ok := byID[id]; ok {
			out = append(out, *row)
		}
	}
	return out
}

// dedupeEdgeKinds returns a stable, dedup'd copy of kinds with empty
// values removed.
func dedupeEdgeKinds(kinds []graph.EdgeKind) []graph.EdgeKind {
	if len(kinds) == 0 {
		return nil
	}
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

// edgeKindSliceToAny converts an EdgeKind slice to []any for Kuzu
// parameter binding (which expects []any for IN-list parameters).
func edgeKindSliceToAny(kinds []graph.EdgeKind) []any {
	out := make([]any, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, string(k))
	}
	return out
}
