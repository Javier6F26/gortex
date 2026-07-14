package store_pg

import (
	"github.com/zzet/gortex/internal/graph"
)

// Git-churn enrichment sidecar.

var (
	_ graph.ChurnEnrichmentWriter = (*Store)(nil)
	_ graph.ChurnEnrichmentReader = (*Store)(nil)
)

func (s *Store) BulkSetChurn(repoPrefix string, rows []graph.ChurnEnrichment) error {
	if s.refuseWrite("BulkSetChurn") { return ErrReadOnlyStore }
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
			`INSERT INTO churn_enrichment (node_id, repo_prefix, commit_count, age_days, churn_rate, last_author, last_commit_at, head_sha, branch, computed_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			 ON CONFLICT (node_id) DO UPDATE SET
			   commit_count = EXCLUDED.commit_count, age_days = EXCLUDED.age_days,
			   churn_rate = EXCLUDED.churn_rate, last_author = EXCLUDED.last_author,
			   last_commit_at = EXCLUDED.last_commit_at`,
			r.NodeID, repoPrefix, r.CommitCount, r.AgeDays, r.ChurnRate,
			r.LastAuthor, r.LastCommitAt, r.HeadSHA, r.Branch, r.ComputedAt); err != nil {
			_ = tx.Rollback(s.ctx)
			return err
		}
	}
	return tx.Commit(s.ctx)
}

func (s *Store) DeleteChurn(nodeIDs []string) error {
	if s.refuseWrite("DeleteChurn") { return ErrReadOnlyStore }
	if len(nodeIDs) == 0 {
		return nil
	}
	_, err := s.pool.Exec(s.ctx, `DELETE FROM churn_enrichment WHERE node_id = ANY($1)`, nodeIDs)
	return err
}

func (s *Store) ChurnRows(repoPrefix string) []graph.ChurnEnrichment {
	q := `SELECT node_id, repo_prefix, commit_count, age_days, churn_rate, last_author, last_commit_at, head_sha, branch, computed_at FROM churn_enrichment`
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

	var out []graph.ChurnEnrichment
	for rows.Next() {
		var r graph.ChurnEnrichment
		if err := rows.Scan(&r.NodeID, &r.RepoPrefix, &r.CommitCount, &r.AgeDays, &r.ChurnRate,
			&r.LastAuthor, &r.LastCommitAt, &r.HeadSHA, &r.Branch, &r.ComputedAt); err != nil {
			return out
		}
		out = append(out, r)
	}
	return out
}
