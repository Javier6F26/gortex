package store_pg

import (
	"fmt"
	"iter"
	"strings"

	"github.com/zzet/gortex/internal/graph"
)

// This file implements the SQL aggregator / scanner optional capability
// interfaces from graph.Store. Each method pushes its GROUP BY / WHERE /
// COUNT into PostgreSQL so the planner drives it through the schema's
// secondary indexes, returning only the aggregate rows instead of
// materialising the whole node / edge table Go-side.

var (
	_ graph.InEdgeCounter            = (*Store)(nil)
	_ graph.NodeIDsByKinds           = (*Store)(nil)
	_ graph.EdgeKindCounter          = (*Store)(nil)
	_ graph.NodeDegreeByKinds        = (*Store)(nil)
	_ graph.NodesInFilesByKindFinder = (*Store)(nil)
	_ graph.FileImportAggregator     = (*Store)(nil)
	_ graph.InDegreeForNodes         = (*Store)(nil)
	_ graph.CrossRepoEdgeAggregator  = (*Store)(nil)
	_ graph.FileImporters            = (*Store)(nil)
	_ graph.FileSymbolNamesByPaths   = (*Store)(nil)
	_ graph.EdgesByKindsScanner      = (*Store)(nil)
	_ graph.NodesByKindsScanner      = (*Store)(nil)
	_ graph.EdgeAdjacencyForKinds    = (*Store)(nil)
	_ graph.NodeDegreeAggregator     = (*Store)(nil)
	_ graph.NodeFanAggregator        = (*Store)(nil)
	_ graph.ExternalCallCandidates   = (*Store)(nil)
	_ graph.CommunityCrossingsByKind = (*Store)(nil)
)

// InEdgeCountsByKind returns per-target incoming-edge counts filtered by kind.
func (s *Store) InEdgeCountsByKind(kinds []graph.EdgeKind) map[string]int {
	if len(kinds) == 0 {
		return nil
	}
	return s.queryStringIntMap(
		`SELECT to_id, COUNT(*) FROM edges WHERE kind = ANY($1) GROUP BY to_id`,
		kindStrings(kinds))
}

// NodeIDsByKinds returns just the IDs of nodes whose kind is in the supplied set.
func (s *Store) NodeIDsByKinds(kinds []graph.NodeKind) []string {
	if len(kinds) == 0 {
		return nil
	}
	rows, err := s.pool.Query(s.ctx,
		`SELECT id FROM nodes WHERE kind = ANY($1)`, kindStringsNode(kinds))
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return out
		}
		out = append(out, id)
	}
	return out
}

// EdgeKindCounts returns one row per distinct edge kind with its count.
func (s *Store) EdgeKindCounts() map[graph.EdgeKind]int {
	out := make(map[graph.EdgeKind]int)
	rows, err := s.pool.Query(s.ctx, `SELECT kind, COUNT(*) FROM edges GROUP BY kind`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			return out
		}
		out[graph.EdgeKind(kind)] = n
	}
	return out
}

// NodeDegreeByKinds returns per-node total in/out edge counts for nodes
// whose kind is in the supplied set.
func (s *Store) NodeDegreeByKinds(kinds []graph.NodeKind, pathPrefix string) []graph.NodeDegreeRow {
	if len(kinds) == 0 {
		return nil
	}

	pathFilter := ""
	var args []any
	args = append(args, kindStringsNode(kinds))
	if pathPrefix != "" {
		pathFilter = fmt.Sprintf(" AND n.file_path LIKE $%d || '%%'", len(args)+1)
		args = append(args, pathPrefix)
	}

	q := fmt.Sprintf(`
		SELECT n.id,
			COALESCE((SELECT COUNT(*) FROM edges e WHERE e.to_id = n.id), 0) AS in_count,
			COALESCE((SELECT COUNT(*) FROM edges e WHERE e.from_id = n.id), 0) AS out_count,
			0 AS usage_in_count
		FROM nodes n
		WHERE n.kind = ANY($1)%s`, pathFilter)

	rows, err := s.pool.Query(s.ctx, q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []graph.NodeDegreeRow
	for rows.Next() {
		var r graph.NodeDegreeRow
		if err := rows.Scan(&r.NodeID, &r.InCount, &r.OutCount, &r.UsageInCount); err != nil {
			return out
		}
		out = append(out, r)
	}
	return out
}

// NodesInFilesByKind returns nodes of specific kinds in specific files.
func (s *Store) NodesInFilesByKind(files []string, kinds []graph.NodeKind) []*graph.Node {
	if len(files) == 0 || len(kinds) == 0 {
		return nil
	}
	return s.queryNodes(s.ctx,
		`SELECT `+nodeCols+` FROM nodes WHERE file_path = ANY($1) AND kind = ANY($2)`,
		files, kindStringsNode(kinds))
}

// FileImportCounts returns per-target-file import counts.
func (s *Store) FileImportCounts(scope []string) []graph.FileImportCountRow {
	if scope != nil && len(scope) == 0 {
		return nil
	}

	base := `SELECT COALESCE(NULLIF(n.file_path, ''), n.id) AS path, COUNT(*) AS cnt
		FROM edges e JOIN nodes n ON e.to_id = n.id
		WHERE e.kind = 'imports'`
	fileToCount := make(map[string]int)

	if scope == nil {
		s.scanImportCountsInline(base+" GROUP BY path", fileToCount)
	} else {
		uniq := dedupeNonEmpty(scope)
		if len(uniq) == 0 {
			return nil
		}
		for i := 0; i < len(uniq); i += lookupChunkSize {
			end := minInt(i+lookupChunkSize, len(uniq))
			chunk := uniq[i:end]
			placeholders := make([]string, len(chunk))
			chunkArgs := make([]any, len(chunk))
			for j, id := range chunk {
				placeholders[j] = fmt.Sprintf("$%d", j+1)
				chunkArgs[j] = id
			}
			q := base + ` AND e.to_id IN (` + strings.Join(placeholders, ",") + `) GROUP BY path`
			s.scanImportCounts(q, chunkArgs, fileToCount)
		}
	}
	if len(fileToCount) == 0 {
		return nil
	}
	out := make([]graph.FileImportCountRow, 0, len(fileToCount))
	for path, cnt := range fileToCount {
		out = append(out, graph.FileImportCountRow{FilePath: path, Count: cnt})
	}
	return out
}

func (s *Store) scanImportCounts(q string, args []any, acc map[string]int) {
	rows, err := s.pool.Query(s.ctx, q, args...)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		var cnt int
		if err := rows.Scan(&path, &cnt); err != nil {
			return
		}
		acc[path] += cnt
	}
}

// InDegreeForNodes returns per-target incoming-edge counts for the given node IDs.
func (s *Store) InDegreeForNodes(ids []string) map[string]int {
	if len(ids) == 0 {
		return nil
	}
	uniq := dedupeNonEmpty(ids)
	return s.queryStringIntMap(
		`SELECT to_id, COUNT(*) FROM edges WHERE to_id = ANY($1) GROUP BY to_id`, uniq)
}

// CrossRepoEdgeCounts returns aggregated cross-repo edge counts.
func (s *Store) CrossRepoEdgeCounts() []graph.CrossRepoEdgeRow {
	rows, err := s.pool.Query(s.ctx,
		`SELECT e.kind, fn.repo_prefix AS from_repo, tn.repo_prefix AS to_repo, COUNT(*) AS cnt
		 FROM edges e
		 JOIN nodes fn ON fn.id = e.from_id
		 JOIN nodes tn ON tn.id = e.to_id
		 WHERE fn.repo_prefix <> '' AND tn.repo_prefix <> '' AND fn.repo_prefix <> tn.repo_prefix
		 GROUP BY e.kind, fn.repo_prefix, tn.repo_prefix`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []graph.CrossRepoEdgeRow
	for rows.Next() {
		var r graph.CrossRepoEdgeRow
		if err := rows.Scan(&r.Kind, &r.FromRepo, &r.ToRepo, &r.Count); err != nil {
			return out
		}
		out = append(out, r)
	}
	return out
}

// FileImporters returns files that import the given file.
func (s *Store) FileImporters(filePath string) []graph.FileImporterRow {
	rows, err := s.pool.Query(s.ctx,
		`SELECT nf.file_path, nf.id, nf.name, nf.kind
		 FROM edges e
		 JOIN nodes nt ON e.to_id = nt.id
		 JOIN nodes nf ON e.from_id = nf.id
		 WHERE e.kind = 'imports' AND (nt.file_path = $1 OR nt.id = $2)
		 ORDER BY nf.file_path`, filePath, filePath)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []graph.FileImporterRow
	for rows.Next() {
		var r graph.FileImporterRow
		var kind string
		if err := rows.Scan(&r.FromFile, &r.FromID, &r.FromName, &kind); err != nil {
			return out
		}
		r.FromKind = graph.NodeKind(kind)
		out = append(out, r)
	}
	return out
}

// FileSymbolNamesByPaths returns distinct symbol names for specific files.
func (s *Store) FileSymbolNamesByPaths(paths []string, kinds []graph.NodeKind) []graph.FileSymbolNameRow {
	if len(paths) == 0 || len(kinds) == 0 {
		return nil
	}
	rows, err := s.pool.Query(s.ctx,
		`SELECT file_path, name FROM nodes WHERE file_path = ANY($1) AND kind = ANY($2) ORDER BY file_path, name`,
		paths, kindStringsNode(kinds))
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []graph.FileSymbolNameRow
	for rows.Next() {
		var r graph.FileSymbolNameRow
		if err := rows.Scan(&r.FilePath, &r.Name); err != nil {
			return out
		}
		out = append(out, r)
	}
	return out
}

// EdgesByKinds streams every edge whose Kind is in the supplied set.
func (s *Store) EdgesByKinds(kinds []graph.EdgeKind) iter.Seq[*graph.Edge] {
	return func(yield func(*graph.Edge) bool) {
		out := s.queryEdges(s.ctx,
			`SELECT `+edgeCols+` FROM edges WHERE kind = ANY($1)`, kindStrings(kinds))
		for _, e := range out {
			if !yield(e) {
				return
			}
		}
	}
}

// ExternalCallCandidateEdges returns edges eligible for external-call synthesis.
func (s *Store) ExternalCallCandidateEdges() []*graph.Edge {
	return s.queryEdges(s.ctx,
		`SELECT `+edgeCols+` FROM edges WHERE (kind = 'calls' OR kind = 'references') AND
		 (to_id LIKE 'dep::%' OR to_id LIKE 'stdlib::%' OR to_id LIKE 'external::%' OR to_id LIKE 'external-call::%')`)
}

// NodesByKinds returns all nodes whose kinds are in the supplied set.
func (s *Store) NodesByKinds(kinds []graph.NodeKind) []*graph.Node {
	if len(kinds) == 0 {
		return nil
	}
	return s.queryNodes(s.ctx,
		`SELECT `+nodeCols+` FROM nodes WHERE kind = ANY($1)`, kindStringsNode(kinds))
}

// EdgeAdjacencyForKinds streams (from, to) pairs for edges matching the
// criteria.
func (s *Store) EdgeAdjacencyForKinds(edgeKinds []graph.EdgeKind, nodeKinds []graph.NodeKind) iter.Seq[[2]string] {
	return func(yield func([2]string) bool) {
		if len(edgeKinds) == 0 || len(nodeKinds) == 0 {
			return
		}
		rows, err := s.pool.Query(s.ctx,
			`SELECT DISTINCT e.from_id, e.to_id
			 FROM edges e
			 JOIN nodes fn ON fn.id = e.from_id
			 JOIN nodes tn ON tn.id = e.to_id
			 WHERE e.kind = ANY($1) AND fn.kind = ANY($2) AND tn.kind = ANY($2)`,
			kindStrings(edgeKinds), kindStringsNode(nodeKinds))
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var pair [2]string
			if err := rows.Scan(&pair[0], &pair[1]); err != nil {
				return
			}
			if !yield(pair) {
				return
			}
		}
	}
}

// NodeDegreeCounts returns per-node in/out/usage degree counts.
func (s *Store) NodeDegreeCounts(ids []string, usageKinds []graph.EdgeKind) []graph.NodeDegreeRow {
	if len(ids) == 0 {
		return nil
	}
	uniq := dedupeNonEmpty(ids)
	rows, err := s.pool.Query(s.ctx,
		`SELECT n.id,
			COALESCE((SELECT COUNT(*) FROM edges e WHERE e.to_id = n.id), 0) AS in_count,
			COALESCE((SELECT COUNT(*) FROM edges e WHERE e.from_id = n.id), 0) AS out_count,
			COALESCE((SELECT COUNT(*) FROM edges e WHERE e.to_id = n.id AND e.kind = ANY($2)), 0) AS usage_in_count
		 FROM nodes n
		 WHERE n.id = ANY($1)`, uniq, kindStrings(usageKinds))
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []graph.NodeDegreeRow
	for rows.Next() {
		var r graph.NodeDegreeRow
		if err := rows.Scan(&r.NodeID, &r.InCount, &r.OutCount, &r.UsageInCount); err != nil {
			return out
		}
		out = append(out, r)
	}
	return out
}

// NodeFanCounts returns per-node fan-in/fan-out counts filtered by edge kind.
func (s *Store) NodeFanCounts(ids []string, fanInKinds, fanOutKinds []graph.EdgeKind) []graph.NodeFanRow {
	if len(ids) == 0 {
		return nil
	}
	uniq := dedupeNonEmpty(ids)
	rows, err := s.pool.Query(s.ctx,
		`SELECT n.id,
			COALESCE((SELECT COUNT(*) FROM edges e WHERE e.to_id = n.id AND e.kind = ANY($2)), 0) AS fan_in,
			COALESCE((SELECT COUNT(*) FROM edges e WHERE e.from_id = n.id AND e.kind = ANY($3)), 0) AS fan_out
		 FROM nodes n
		 WHERE n.id = ANY($1)`, uniq, kindStrings(fanInKinds), kindStrings(fanOutKinds))
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []graph.NodeFanRow
	for rows.Next() {
		var r graph.NodeFanRow
		if err := rows.Scan(&r.NodeID, &r.FanIn, &r.FanOut); err != nil {
			return out
		}
		out = append(out, r)
	}
	return out
}

// CommunityCrossingsByKind returns per-source crossing counts for edges
// of the given kinds.
func (s *Store) CommunityCrossingsByKind(kinds []graph.EdgeKind, nodeToComm map[string]string) map[string]int {
	if len(kinds) == 0 || len(nodeToComm) == 0 {
		return nil
	}

	// Build a reverse map: community → []nodeID
	commToNodes := make(map[string][]string)
	for nodeID, comm := range nodeToComm {
		commToNodes[comm] = append(commToNodes[comm], nodeID)
	}

	var out map[string]int
	for _, nodes := range commToNodes {
		uniq := dedupeNonEmpty(nodes)
		rows, err := s.pool.Query(s.ctx,
			`SELECT e.from_id, COUNT(*) FROM edges e
			 WHERE e.kind = ANY($1) AND e.from_id = ANY($2)
			 GROUP BY e.from_id`,
			kindStrings(kinds), uniq)
		if err != nil {
			return out
		}
		for rows.Next() {
			var fromID string
			var count int
			if err := rows.Scan(&fromID, &count); err != nil {
				rows.Close()
				return out
			}
			// Only count edges whose target is in a different community.
			targetRows, err := s.pool.Query(s.ctx,
				`SELECT e.to_id FROM edges e WHERE e.kind = ANY($1) AND e.from_id = $2`,
				kindStrings(kinds), fromID)
			if err != nil {
				rows.Close()
				return out
			}
			var crossingCount int
			for targetRows.Next() {
				var toID string
				if err := targetRows.Scan(&toID); err != nil {
					targetRows.Close()
					rows.Close()
					return out
				}
				if nodeToComm[toID] != nodeToComm[fromID] {
					crossingCount++
				}
			}
			targetRows.Close()
			if crossingCount > 0 {
				if out == nil {
					out = make(map[string]int)
				}
				out[fromID] = crossingCount
			}
		}
		rows.Close()
	}
	return out
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func (s *Store) queryStringIntMap(q string, args any) map[string]int {
	rows, err := s.pool.Query(s.ctx, q, args)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var key string
		var n int
		if err := rows.Scan(&key, &n); err != nil {
			return out
		}
		out[key] = n
	}
	return out
}

func kindStrings(kinds []graph.EdgeKind) []string {
	out := make([]string, len(kinds))
	for i, k := range kinds {
		out[i] = string(k)
	}
	return out
}

func kindStringsNode(kinds []graph.NodeKind) []string {
	out := make([]string, len(kinds))
	for i, k := range kinds {
		out[i] = string(k)
	}
	return out
}


func (s *Store) scanImportCountsInline(q string, acc map[string]int) {
    rows, err := s.pool.Query(s.ctx, q)
    if err != nil {
        return
    }
    defer rows.Close()
    for rows.Next() {
        var path string
        var cnt int
        if err := rows.Scan(&path, &cnt); err != nil {
            return
        }
        acc[path] += cnt
    }
}
