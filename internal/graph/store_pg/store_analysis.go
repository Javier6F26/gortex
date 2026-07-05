package store_pg

import (
	"fmt"

	"github.com/zzet/gortex/internal/graph"
)

// This file implements the analysis-oriented optional capability interfaces
// from graph.Store: DeadCodeCandidator, IfaceImplementsScanner,
// MemberMethodsByType, StructuralParentEdges, ExtractCandidatesScanner,
// CrossRepoCandidates, ThrowerErrorSurfacer.

var (
	_ graph.DeadCodeCandidator       = (*Store)(nil)
	_ graph.IfaceImplementsScanner   = (*Store)(nil)
	_ graph.MemberMethodsByType      = (*Store)(nil)
	_ graph.StructuralParentEdges    = (*Store)(nil)
	_ graph.ExtractCandidatesScanner = (*Store)(nil)
	_ graph.CrossRepoCandidates      = (*Store)(nil)
	_ graph.ThrowerErrorSurfacer     = (*Store)(nil)
)

// DeadCodeCandidates returns nodes with no incoming edges of the allowed kinds.
func (s *Store) DeadCodeCandidates(allowedNodeKinds []graph.NodeKind, allowedInEdgeKinds map[graph.NodeKind][]graph.EdgeKind) []*graph.Node {
	if len(allowedNodeKinds) == 0 {
		return nil
	}
	var out []*graph.Node
	for _, nk := range allowedNodeKinds {
		allowedEdges := allowedInEdgeKinds[nk]
		var nodes []*graph.Node
		if len(allowedEdges) == 0 {
			nodes = s.queryNodes(s.ctx,
				`SELECT n.`+nodeCols+` FROM nodes n
				 WHERE n.kind = $1
				 AND NOT EXISTS (SELECT 1 FROM edges e WHERE e.to_id = n.id)`,
				string(nk))
		} else {
			nodes = s.queryNodes(s.ctx,
				`SELECT n.`+nodeCols+` FROM nodes n
				 WHERE n.kind = $1
				 AND NOT EXISTS (SELECT 1 FROM edges e WHERE e.to_id = n.id AND e.kind = ANY($2))`,
				string(nk), kindStrings(allowedEdges))
		}
		out = append(out, nodes...)
	}
	return out
}

// IfaceImplementsRows returns all (typeID, interfaceID, interfaceMeta) tuples.
func (s *Store) IfaceImplementsRows() []graph.IfaceImplementsRow {
	rows, err := s.pool.Query(s.ctx,
		`SELECT e.from_id, e.to_id, tn.meta
		 FROM edges e
		 JOIN nodes tn ON tn.id = e.to_id
		 WHERE e.kind = 'implements' AND tn.kind = 'interface' AND tn.meta IS NOT NULL`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []graph.IfaceImplementsRow
	for rows.Next() {
		var r graph.IfaceImplementsRow
		var metaBlob []byte
		if err := rows.Scan(&r.TypeID, &r.IfaceID, &metaBlob); err != nil {
			return out
		}
		if len(metaBlob) > 0 {
			m, _ := decodeMeta(metaBlob)
			r.IfaceMeta = m
		}
		out = append(out, r)
	}
	return out
}

// MemberMethodsByType returns typeID -> []MemberMethodInfo.
func (s *Store) MemberMethodsByType() map[string][]graph.MemberMethodInfo {
	rows, err := s.pool.Query(s.ctx,
		`SELECT e.to_id, n.id, n.name, n.file_path, n.start_line, n.repo_prefix
		 FROM edges e
		 JOIN nodes n ON n.id = e.from_id
		 WHERE e.kind = 'member_of' AND n.kind = 'method'`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out map[string][]graph.MemberMethodInfo
	seen := make(map[[2]string]bool)
	for rows.Next() {
		var typeID, methodID, name, filePath, repoPrefix string
		var startLine int
		if err := rows.Scan(&typeID, &methodID, &name, &filePath, &startLine, &repoPrefix); err != nil {
			return out
		}
		if out == nil {
			out = make(map[string][]graph.MemberMethodInfo)
		}
		key := [2]string{typeID, methodID}
		if seen[key] {
			continue
		}
		seen[key] = true
		out[typeID] = append(out[typeID], graph.MemberMethodInfo{
			MethodID: methodID, Name: name, FilePath: filePath,
			StartLine: startLine, RepoPrefix: repoPrefix,
		})
	}
	return out
}

// StructuralParentEdges returns every extends/implements/composes edge.
func (s *Store) StructuralParentEdges() []graph.StructuralParentEdgeRow {
	rows, err := s.pool.Query(s.ctx,
		`SELECT e.from_id, e.to_id, fn.kind, tn.kind, e.origin
		 FROM edges e
		 JOIN nodes fn ON fn.id = e.from_id
		 JOIN nodes tn ON tn.id = e.to_id
		 WHERE e.kind IN ('extends', 'implements', 'composes')
		 AND fn.kind IN ('type', 'interface')
		 AND tn.kind IN ('type', 'interface')`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []graph.StructuralParentEdgeRow
	for rows.Next() {
		var r graph.StructuralParentEdgeRow
		if err := rows.Scan(&r.FromID, &r.ToID, &r.FromKind, &r.ToKind, &r.Origin); err != nil {
			return out
		}
		out = append(out, r)
	}
	return out
}

// ExtractCandidates returns candidates for extraction ranking.
func (s *Store) ExtractCandidates(kinds []graph.EdgeKind, minLines, minCallers, minFanOut int, pathPrefix string) []graph.ExtractCandidateRow {
	if len(kinds) == 0 {
		return nil
	}

	q := `SELECT n.id, n.name, n.file_path, n.start_line, n.end_line,
	             n.end_line - n.start_line + 1 AS line_count,
	             COALESCE((SELECT COUNT(DISTINCT e.from_id) FROM edges e WHERE e.to_id = n.id AND e.kind = ANY($1)), 0) AS caller_count,
	             COALESCE((SELECT COUNT(DISTINCT e.to_id) FROM edges e WHERE e.from_id = n.id AND e.kind = ANY($1)), 0) AS fan_out
	      FROM nodes n
	      WHERE n.start_line > 0 AND n.end_line > 0
	        AND (n.end_line - n.start_line + 1) >= $2`

	var args []any
	args = append(args, kindStrings(kinds), minLines)

	argIdx := 3
	if pathPrefix != "" {
		q += fmt.Sprintf(" AND n.file_path LIKE $%d || '%%'", argIdx)
		args = append(args, pathPrefix)
		argIdx++
	}

	q += fmt.Sprintf(` GROUP BY n.id, n.name, n.file_path, n.start_line, n.end_line HAVING
		COALESCE((SELECT COUNT(DISTINCT e.from_id) FROM edges e WHERE e.to_id = n.id AND e.kind = ANY($1)), 0) >= $%d
		AND COALESCE((SELECT COUNT(DISTINCT e.to_id) FROM edges e WHERE e.from_id = n.id AND e.kind = ANY($1)), 0) >= $%d`,
		argIdx, argIdx+1)
	args = append(args, minCallers, minFanOut)

	rows, err := s.pool.Query(s.ctx, q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []graph.ExtractCandidateRow
	for rows.Next() {
		var r graph.ExtractCandidateRow
		if err := rows.Scan(&r.NodeID, &r.Name, &r.FilePath, &r.StartLine, &r.EndLine,
			&r.LineCount, &r.CallerCount, &r.FanOut); err != nil {
			return out
		}
		out = append(out, r)
	}
	return out
}

// CrossRepoCandidates returns edges with cross-repo parallel kinds.
func (s *Store) CrossRepoCandidates(baseKinds []graph.EdgeKind) []graph.CrossRepoCandidateRow {
	if len(baseKinds) == 0 {
		return nil
	}

	rows, err := s.pool.Query(s.ctx,
		`SELECT e.from_id, e.to_id, e.kind, e.file_path, e.line,
		        e.confidence, e.confidence_label, e.origin, e.tier,
		        e.cross_repo, e.meta,
		        fn.repo_prefix AS from_repo, tn.repo_prefix AS to_repo
		 FROM edges e
		 JOIN nodes fn ON fn.id = e.from_id
		 JOIN nodes tn ON tn.id = e.to_id
		 WHERE e.kind = ANY($1)
		 AND fn.repo_prefix <> '' AND tn.repo_prefix <> ''
		 AND fn.repo_prefix <> tn.repo_prefix`, kindStrings(baseKinds))
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []graph.CrossRepoCandidateRow
	for rows.Next() {
		var e graph.Edge
		var metaBlob []byte
		var fromRepo, toRepo string

		if err := rows.Scan(&e.From, &e.To, &e.Kind, &e.FilePath, &e.Line,
			&e.Confidence, &e.ConfidenceLabel, &e.Origin, &e.Tier,
			&e.CrossRepo, &metaBlob, &fromRepo, &toRepo); err != nil {
			return out
		}
		if len(metaBlob) > 0 {
			m, _ := decodeMeta(metaBlob)
			e.Meta = m
		}
		out = append(out, graph.CrossRepoCandidateRow{
			Edge: &e, FromRepo: fromRepo, ToRepo: toRepo,
		})
	}
	return out
}

// ThrowerErrorSurface returns per-thrower error surfaces.
func (s *Store) ThrowerErrorSurface(pathPrefix string) []graph.ThrowerErrorRow {
	q := `SELECT e.from_id, n.file_path, n.start_line, COUNT(*) AS throws
	      FROM edges e
	      JOIN nodes n ON n.id = e.from_id
	      WHERE e.kind = 'throws'`

	var args []any
	if pathPrefix != "" {
		q += ` AND n.file_path LIKE $1 || '%'`
		args = append(args, pathPrefix)
	}
	q += ` GROUP BY e.from_id, n.file_path, n.start_line`

	rows, err := s.pool.Query(s.ctx, q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	type throwerInfo struct {
		throwerID string
		filePath  string
		line      int
		throws    int
	}
	var throwers []throwerInfo
	for rows.Next() {
		var t throwerInfo
		if err := rows.Scan(&t.throwerID, &t.filePath, &t.line, &t.throws); err != nil {
			return nil
		}
		throwers = append(throwers, t)
	}
	if len(throwers) == 0 {
		return nil
	}

	out := make([]graph.ThrowerErrorRow, 0, len(throwers))
	for _, t := range throwers {
		row := graph.ThrowerErrorRow{
			ThrowerID: t.throwerID, FilePath: t.filePath,
			Line: t.line, Throws: t.throws,
		}
		seenTarget := make(map[string]bool)
		for _, e := range s.GetOutEdges(t.throwerID) {
			if e.Kind == "throws" && !seenTarget[e.To] {
				seenTarget[e.To] = true
				row.ErrorTargets = append(row.ErrorTargets, e.To)
			}
		}
		seenMsg := make(map[string]bool)
		for _, e := range s.GetOutEdges(t.throwerID) {
			if e.Kind == "emits" {
				n := s.GetNode(e.To)
				if n != nil && n.Kind == "string" {
					if v, ok := n.Meta["context"]; ok && v == "error_msg" && !seenMsg[n.Name] {
						seenMsg[n.Name] = true
						row.ErrorMsgs = append(row.ErrorMsgs, n.Name)
					}
				}
			}
		}
		out = append(out, row)
	}
	return out
}
