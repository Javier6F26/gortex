package store_pg

import (
	"github.com/zzet/gortex/internal/graph"
)

// Constant value persistence for const-identifier dispatch name resolution.

var (
	_ graph.ConstantValueWriter = (*Store)(nil)
	_ graph.ConstantValueReader = (*Store)(nil)
)

func (s *Store) BulkSetConstantValues(repoPrefix string, rows []graph.ConstantValueRow) error {
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
		if r.NodeID == "" {
			continue
		}
		if _, err := tx.Exec(s.ctx,
			`INSERT INTO constant_values (node_id, repo_prefix, file_path, value) VALUES ($1, $2, $3, $4)
			 ON CONFLICT (node_id) DO UPDATE SET value = EXCLUDED.value, file_path = EXCLUDED.file_path`,
			r.NodeID, repoPrefix, r.FilePath, r.Value); err != nil {
			_ = tx.Rollback(s.ctx)
			return err
		}
	}
	return tx.Commit(s.ctx)
}

func (s *Store) DeleteConstantValuesByFiles(repoPrefix string, files []string) error {
	if len(files) == 0 {
		return nil
	}
	_, err := s.pool.Exec(s.ctx,
		`DELETE FROM constant_values WHERE repo_prefix = $1 AND file_path = ANY($2)`, repoPrefix, files)
	return err
}

func (s *Store) ConstantValuesByNodeIDs(nodeIDs []string) (map[string]string, error) {
	if len(nodeIDs) == 0 {
		return map[string]string{}, nil
	}
	uniq := dedupeNonEmpty(nodeIDs)
	rows, err := s.pool.Query(s.ctx,
		`SELECT node_id, value FROM constant_values WHERE node_id = ANY($1)`, uniq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]string, len(uniq))
	for rows.Next() {
		var nodeID, value string
		if err := rows.Scan(&nodeID, &value); err != nil {
			return out, err
		}
		out[nodeID] = value
	}
	return out, rows.Err()
}
