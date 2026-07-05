package store_pg

import (
	"fmt"
	"strings"

	"github.com/zzet/gortex/internal/graph"
)

// This file implements graph.ContentSearcher on the PostgreSQL backend using
// the content_fts table with a tsvector search_body column and GIN index.
// It replaces SQLite's content_fts FTS5 virtual table.
//
// Content bodies are stored in the content_fts table with a generated
// tsvector column (search_body) derived from the English text search
// configuration. Queries use plainto_tsquery for simple phrase matching
// and ts_rank / ts_headline for scoring and snippets.

// Compile-time assertion: *Store satisfies the content-search capability.
var _ graph.ContentSearcher = (*Store)(nil)

// WipeContent removes a repo's content rows before a full rebuild.
func (s *Store) WipeContent(repoPrefix string) error {
	_, err := s.pool.Exec(s.ctx, `DELETE FROM content_fts WHERE repo_prefix = $1`, repoPrefix)
	return err
}

// WipeContentFile removes one file's content rows.
func (s *Store) WipeContentFile(filePath string) error {
	if filePath == "" {
		return nil
	}
	_, err := s.pool.Exec(s.ctx, `DELETE FROM content_fts WHERE file_path = $1`, filePath)
	return err
}

// AppendContent inserts content rows for repoPrefix without wiping.
func (s *Store) AppendContent(repoPrefix string, items []graph.ContentFTSItem) error {
	if len(items) == 0 {
		return nil
	}

	// Chunked insert to stay within parameter limits.
	const contentChunkRows = 180
	for i := 0; i < len(items); i += contentChunkRows {
		end := minInt(i+contentChunkRows, len(items))
		chunk := items[i:end]

		valueStrings := make([]string, 0, len(chunk))
		valueArgs := make([]any, 0, len(chunk)*5)
		for _, item := range chunk {
			if item.NodeID == "" {
				continue
			}
			offset := len(valueStrings) * 5
			valueStrings = append(valueStrings,
				fmt.Sprintf("($%d,$%d,$%d,$%d,$%d)", offset+1, offset+2, offset+3, offset+4, offset+5))
			sanitized := strings.ToValidUTF8(item.Body, "�")
			valueArgs = append(valueArgs, item.NodeID, repoPrefix, item.FilePath, item.Ordinal, sanitized)
		}
		if len(valueStrings) == 0 {
			continue
		}

		q := `INSERT INTO content_fts (node_id, repo_prefix, file_path, ordinal, body) VALUES ` +
			strings.Join(valueStrings, ",") +
			` ON CONFLICT DO NOTHING`
		if _, err := s.pool.Exec(s.ctx, q, valueArgs...); err != nil {
			return fmt.Errorf("store_pg: append content: %w", err)
		}
	}
	return nil
}

// SearchContent runs a plain text query against content bodies using tsvector.
func (s *Store) SearchContent(query, repoPrefix string, limit int) ([]graph.ContentHit, error) {
	if query == "" || limit <= 0 {
		return nil, nil
	}

	q := `SELECT node_id, file_path, ordinal,
	             ts_rank(search_body, plainto_tsquery('english', $1)) AS score,
	             ts_headline('english', body, plainto_tsquery('english', $1), 'MaxWords=40,MinWords=20') AS snippet
	      FROM content_fts
	      WHERE search_body @@ plainto_tsquery('english', $1)`

	var args []any
	args = append(args, query)

	if repoPrefix != "" {
		q += ` AND repo_prefix = $2`
		args = append(args, repoPrefix)
	}

	q += ` ORDER BY score DESC LIMIT $` + fmt.Sprintf("%d", len(args)+1)
	args = append(args, limit)

	rows, err := s.pool.Query(s.ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store_pg: search content: %w", err)
	}
	defer rows.Close()

	var hits []graph.ContentHit
	for rows.Next() {
		var h graph.ContentHit
		if err := rows.Scan(&h.NodeID, &h.FilePath, &h.Ordinal, &h.Score, &h.Snippet); err != nil {
			return hits, err
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// BuildContentIndex ensures the GIN index on the tsvector column exists.
func (s *Store) BuildContentIndex() error {
	_, err := s.pool.Exec(s.ctx, `CREATE INDEX IF NOT EXISTS idx_content_fts_gin ON content_fts USING GIN (search_body)`)
	return err
}

// ScanContent streams every stored content row (scoped to repoPrefix) to fn.
func (s *Store) ScanContent(repoPrefix string, fn func(nodeID, filePath, body string) bool) error {
	q := `SELECT node_id, file_path, body FROM content_fts`
	var args []any
	if repoPrefix != "" {
		q += ` WHERE repo_prefix = $1`
		args = append(args, repoPrefix)
	}

	rows, err := s.pool.Query(s.ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var nodeID, filePath, body string
		if err := rows.Scan(&nodeID, &filePath, &body); err != nil {
			return err
		}
		if !fn(nodeID, filePath, body) {
			break
		}
	}
	return rows.Err()
}
