package store_pg

import (
	"github.com/zzet/gortex/internal/graph"
)

// File mtime persistence for incremental reindex decisions.

var (
	_ graph.FileMtimeWriter   = (*Store)(nil)
	_ graph.FileMtimeReader   = (*Store)(nil)
	_ graph.FileMtimeReplacer = (*Store)(nil)
	_ graph.FileMtimeDeleter  = (*Store)(nil)
)

func (s *Store) BulkSetFileMtimes(repoPrefix string, mtimes map[string]int64) error {
	if s.refuseWrite("BulkSetFileMtimes") { return ErrReadOnlyStore }
	if len(mtimes) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.pool.Begin(s.ctx)
	if err != nil {
		return err
	}
	for path, mtime := range mtimes {
		if _, err := tx.Exec(s.ctx,
			`INSERT INTO file_mtimes (repo_prefix, file_path, mtime_ns) VALUES ($1, $2, $3)
			 ON CONFLICT (repo_prefix, file_path) DO UPDATE SET mtime_ns = EXCLUDED.mtime_ns`,
			repoPrefix, path, mtime); err != nil {
			_ = tx.Rollback(s.ctx)
			return err
		}
	}
	return tx.Commit(s.ctx)
}

func (s *Store) ReplaceFileMtimes(repoPrefix string, mtimes map[string]int64) error {
	if s.refuseWrite("ReplaceFileMtimes") { return ErrReadOnlyStore }
	if len(mtimes) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.pool.Begin(s.ctx)
	if err != nil {
		return err
	}
	// Delete all existing mtimes for this repo, then re-insert.
	if _, err := tx.Exec(s.ctx, `DELETE FROM file_mtimes WHERE repo_prefix = $1`, repoPrefix); err != nil {
		_ = tx.Rollback(s.ctx)
		return err
	}
	for path, mtime := range mtimes {
		if _, err := tx.Exec(s.ctx,
			`INSERT INTO file_mtimes (repo_prefix, file_path, mtime_ns) VALUES ($1, $2, $3)`,
			repoPrefix, path, mtime); err != nil {
			_ = tx.Rollback(s.ctx)
			return err
		}
	}
	return tx.Commit(s.ctx)
}

func (s *Store) DeleteFileMtimes(repoPrefix string, paths []string) error {
	if s.refuseWrite("DeleteFileMtimes") { return ErrReadOnlyStore }
	if len(paths) == 0 {
		return nil
	}
	_, err := s.pool.Exec(s.ctx,
		`DELETE FROM file_mtimes WHERE repo_prefix = $1 AND file_path = ANY($2)`, repoPrefix, paths)
	return err
}

func (s *Store) LoadFileMtimes(repoPrefix string) map[string]int64 {
	if repoPrefix == "" {
		return nil
	}
	rows, err := s.pool.Query(s.ctx,
		`SELECT file_path, mtime_ns FROM file_mtimes WHERE repo_prefix = $1`, repoPrefix)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := make(map[string]int64)
	for rows.Next() {
		var path string
		var mtime int64
		if err := rows.Scan(&path, &mtime); err != nil {
			return out
		}
		out[path] = mtime
	}
	return out
}
