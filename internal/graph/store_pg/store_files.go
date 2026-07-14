package store_pg

import (
	"github.com/zzet/gortex/internal/graph"
)

// Per-file metadata persistence for index_health reporting.

var (
	_ graph.FileMetaWriter = (*Store)(nil)
	_ graph.FileMetaReader = (*Store)(nil)
)

func (s *Store) SetFileMetas(repoPrefix string, rows []graph.FileMetaRow) error {
	if s.refuseWrite("SetFileMetas") { return ErrReadOnlyStore }
	if len(rows) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.pool.Begin(s.ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := tx.Exec(s.ctx,
			`INSERT INTO files (repo_prefix, file_path, content_hash, size, node_count, errors)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 ON CONFLICT (repo_prefix, file_path) DO UPDATE SET
			   content_hash = EXCLUDED.content_hash, size = EXCLUDED.size,
			   node_count = EXCLUDED.node_count, errors = EXCLUDED.errors`,
			repoPrefix, r.FilePath, r.ContentHash, r.Size, r.NodeCount, r.Errors); err != nil {
			_ = tx.Rollback(s.ctx)
			return err
		}
	}
	return tx.Commit(s.ctx)
}

func (s *Store) DeleteFileMetasByFiles(repoPrefix string, files []string) error {
	if s.refuseWrite("DeleteFileMetasByFiles") { return ErrReadOnlyStore }
	if len(files) == 0 {
		return nil
	}
	_, err := s.pool.Exec(s.ctx,
		`DELETE FROM files WHERE repo_prefix = $1 AND file_path = ANY($2)`, repoPrefix, files)
	return err
}

func (s *Store) FileMetasForRepo(repoPrefix string) ([]graph.FileMetaRow, error) {
	rows, err := s.pool.Query(s.ctx,
		`SELECT file_path, content_hash, size, node_count, errors FROM files WHERE repo_prefix = $1`, repoPrefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []graph.FileMetaRow
	for rows.Next() {
		var r graph.FileMetaRow
		if err := rows.Scan(&r.FilePath, &r.ContentHash, &r.Size, &r.NodeCount, &r.Errors); err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
