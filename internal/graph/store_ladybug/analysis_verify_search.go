package store_ladybug

import (
	"github.com/zzet/gortex/internal/graph"
)

// Compile-time assertions: *Store satisfies the verify+search
// capability set so the MCP handlers pick the server-side path via
// type assertion. Signature drift breaks the build here instead of
// silently degrading to the AllNodes / AllEdges Go fallback.
var (
	_ graph.FileImporters            = (*Store)(nil)
	_ graph.InEdgeCounter            = (*Store)(nil)
	_ graph.NodesInFilesByKindFinder = (*Store)(nil)
	_ graph.NodesByKindsScanner      = (*Store)(nil)
)

// NodesByKinds runs the multi-kind candidate scan inside Ladybug.
// Replaces the AllNodes()-then-`if n.Kind != allowed` loop used by
// the metadata analyze handlers (todos, stale_code, stale_flags,
// ownership, coverage_gaps, coverage_summary, cgo_users, wasm_users,
// orphan_tables, unreferenced_tables). The legacy path pulled every
// node over cgo on every call — ~70k rows on the gortex workspace —
// just to keep the handful that matched one of a few kinds. The
// Cypher IN-list ships only the matching rows.
//
// One IN query, not a per-kind loop, because every extra round-trip
// is one more cgo crossing. Kinds dedup keeps the IN list tight when
// the caller passes redundant kinds, matching the in-memory reference.
//
// Meta filtering stays in Go: the meta column is a gob-encoded
// base64 STRING so Cypher cannot inspect its inner keys. The
// candidate-set reduction is the win — the meta gate runs against
// the surviving rows on the Go side.
func (s *Store) NodesByKinds(kinds []graph.NodeKind) []*graph.Node {
	if len(kinds) == 0 {
		return nil
	}
	seen := make(map[graph.NodeKind]struct{}, len(kinds))
	allowed := make([]any, 0, len(kinds))
	for _, k := range kinds {
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		allowed = append(allowed, string(k))
	}
	if len(allowed) == 0 {
		return nil
	}
	const q = `MATCH (n:Node) WHERE n.kind IN $kinds RETURN ` + nodeReturnCols
	rows := s.querySelect(q, map[string]any{"kinds": allowed})
	if len(rows) == 0 {
		return nil
	}
	out := make([]*graph.Node, 0, len(rows))
	for _, r := range rows {
		if n := rowToNode(r); n != nil {
			out = append(out, n)
		}
	}
	if len(rows) >= mallocTrimRowThreshold {
		mallocTrim()
	}
	return out
}

// FileImporters runs the importing-files lookup inside Ladybug.
// Replaces the handleCheckReferences AllEdges() loop — that loop
// materialised every edge over cgo (~286k on the gortex workspace)
// plus per-edge GetNode(e.To)+GetNode(e.From), to answer "what
// imports this file?" with a few rows. One Cypher join now ships
// only the matching rows.
//
// The OR on (to.file_path == $f OR to.id == $f) keeps parity with
// the indexer's two import shapes: file-targeted imports point at
// the file node (whose ID is the path), symbol-targeted imports
// land on a symbol whose FilePath equals the path.
func (s *Store) FileImporters(filePath string) []graph.FileImporterRow {
	if filePath == "" {
		return nil
	}
	const q = `
MATCH (from:Node)-[e:Edge]->(to:Node)
WHERE e.kind = $imp
  AND (to.file_path = $f OR to.id = $f)
RETURN from.file_path, from.id, from.name, from.kind`
	rows := s.querySelect(q, map[string]any{
		"imp": string(graph.EdgeImports),
		"f":   filePath,
	})
	if len(rows) == 0 {
		return nil
	}
	out := make([]graph.FileImporterRow, 0, len(rows))
	for _, r := range rows {
		if len(r) < 4 {
			continue
		}
		fromFile, _ := r[0].(string)
		fromID, _ := r[1].(string)
		fromName, _ := r[2].(string)
		fromKind, _ := r[3].(string)
		if fromID == "" {
			continue
		}
		out = append(out, graph.FileImporterRow{
			FromFile: fromFile,
			FromID:   fromID,
			FromName: fromName,
			FromKind: graph.NodeKind(fromKind),
		})
	}
	return out
}

// InEdgeCountsByKind runs the fan-in count inside Ladybug. Replaces
// the AllEdges() loop in handleGetUntestedSymbols — that loop pulled
// every edge over cgo just to bucket the to-id counts of two kinds.
// The Cypher count(*) returns one row per To, so only the surviving
// per-target counts cross cgo.
func (s *Store) InEdgeCountsByKind(kinds []graph.EdgeKind) map[string]int {
	if len(kinds) == 0 {
		return nil
	}
	// Dedup the kinds so the IN list doesn't double-count when the
	// caller passes redundant kinds.
	seen := make(map[graph.EdgeKind]struct{}, len(kinds))
	allowed := make([]any, 0, len(kinds))
	for _, k := range kinds {
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		allowed = append(allowed, string(k))
	}
	const q = `
MATCH ()-[e:Edge]->(n:Node)
WHERE e.kind IN $kinds
RETURN n.id, count(*)`
	rows := s.querySelect(q, map[string]any{"kinds": allowed})
	if len(rows) == 0 {
		return nil
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		if len(r) < 2 {
			continue
		}
		id, _ := r[0].(string)
		if id == "" {
			continue
		}
		// Kuzu returns count(*) as an int64.
		switch v := r[1].(type) {
		case int64:
			out[id] = int(v)
		case int:
			out[id] = v
		case int32:
			out[id] = int(v)
		}
	}
	return out
}

// NodesInFilesByKind runs the file+kind filter inside Ladybug.
// Replaces the AllNodes() pull in find_declaration's
// buildDeclFileIndex — that loop materialised every node over cgo
// (~70k on the gortex workspace) just to keep the few that landed
// in the small set of trigram-match files.
//
// Empty files or empty kinds returns nil — never a whole-graph
// scan. The deduped IN list keeps the engine plan tight even when
// the caller passes a sloppy file or kind list.
func (s *Store) NodesInFilesByKind(files []string, kinds []graph.NodeKind) []*graph.Node {
	if len(files) == 0 || len(kinds) == 0 {
		return nil
	}
	seenFile := make(map[string]struct{}, len(files))
	fileList := make([]any, 0, len(files))
	for _, f := range files {
		if f == "" {
			continue
		}
		if _, ok := seenFile[f]; ok {
			continue
		}
		seenFile[f] = struct{}{}
		fileList = append(fileList, f)
	}
	if len(fileList) == 0 {
		return nil
	}
	seenKind := make(map[graph.NodeKind]struct{}, len(kinds))
	kindList := make([]any, 0, len(kinds))
	for _, k := range kinds {
		if _, ok := seenKind[k]; ok {
			continue
		}
		seenKind[k] = struct{}{}
		kindList = append(kindList, string(k))
	}
	if len(kindList) == 0 {
		return nil
	}
	const q = `
MATCH (n:Node)
WHERE n.file_path IN $files
  AND n.kind IN $kinds
RETURN ` + nodeReturnCols
	rows := s.querySelect(q, map[string]any{
		"files": fileList,
		"kinds": kindList,
	})
	return rowsToNodes(rows)
}
