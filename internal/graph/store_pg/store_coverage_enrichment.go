package store_pg

import (
	"github.com/zzet/gortex/internal/graph"
)

// Code coverage enrichment sidecar.

var (
	_ graph.CoverageEnrichmentWriter = (*Store)(nil)
	_ graph.CoverageEnrichmentReader = (*Store)(nil)
)

func (s *Store) BulkSetCoverage(repoPrefix string, rows []graph.CoverageEnrichment) error {
	if s.refuseWrite("BulkSetCoverage") { return ErrReadOnlyStore }
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
			`INSERT INTO coverage_enrichment (node_id, repo_prefix, coverage_pct, num_stmt, hit)
			 VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (node_id) DO UPDATE SET
			   coverage_pct = EXCLUDED.coverage_pct, num_stmt = EXCLUDED.num_stmt, hit = EXCLUDED.hit`,
			r.NodeID, repoPrefix, r.CoveragePct, r.NumStmt, r.Hit); err != nil {
			_ = tx.Rollback(s.ctx)
			return err
		}
	}
	return tx.Commit(s.ctx)
}

func (s *Store) DeleteCoverage(nodeIDs []string) error {
	if s.refuseWrite("DeleteCoverage") { return ErrReadOnlyStore }
	if len(nodeIDs) == 0 {
		return nil
	}
	_, err := s.pool.Exec(s.ctx, `DELETE FROM coverage_enrichment WHERE node_id = ANY($1)`, nodeIDs)
	return err
}

func (s *Store) CoverageRows(repoPrefix string) []graph.CoverageEnrichment {
	q := `SELECT node_id, repo_prefix, coverage_pct, num_stmt, hit FROM coverage_enrichment`
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

	var out []graph.CoverageEnrichment
	for rows.Next() {
		var r graph.CoverageEnrichment
		if err := rows.Scan(&r.NodeID, &r.RepoPrefix, &r.CoveragePct, &r.NumStmt, &r.Hit); err != nil {
			return out
		}
		out = append(out, r)
	}
	return out
}
