package store_pg

import (
	"github.com/zzet/gortex/internal/graph"
)

// Blame enrichment sidecar (latest author per symbol).

var (
	_ graph.BlameEnrichmentWriter = (*Store)(nil)
	_ graph.BlameEnrichmentReader = (*Store)(nil)
)

func (s *Store) BulkSetBlame(repoPrefix string, rows []graph.BlameEnrichment) error {
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
			`INSERT INTO blame_enrichment (node_id, repo_prefix, commit_sha, email, ts) VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (node_id) DO UPDATE SET commit_sha = EXCLUDED.commit_sha, email = EXCLUDED.email, ts = EXCLUDED.ts`,
			r.NodeID, repoPrefix, r.Commit, r.Email, r.Timestamp); err != nil {
			_ = tx.Rollback(s.ctx)
			return err
		}
	}
	return tx.Commit(s.ctx)
}

func (s *Store) DeleteBlame(nodeIDs []string) error {
	if len(nodeIDs) == 0 {
		return nil
	}
	_, err := s.pool.Exec(s.ctx, `DELETE FROM blame_enrichment WHERE node_id = ANY($1)`, nodeIDs)
	return err
}

func (s *Store) BlameRows(repoPrefix string) []graph.BlameEnrichment {
	q := `SELECT node_id, repo_prefix, commit_sha, email, ts FROM blame_enrichment`
	var args []any
	if repoPrefix != "" {
		q += ` WHERE repo_prefix = $1`
		args = append(args, repoPrefix)
	}
	rows, err := s.pool.Query(s.ctx, q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []graph.BlameEnrichment
	for rows.Next() {
		var r graph.BlameEnrichment
		if err := rows.Scan(&r.NodeID, &r.RepoPrefix, &r.Commit, &r.Email, &r.Timestamp); err != nil {
			return out
		}
		out = append(out, r)
	}
	return out
}
