package store_pg

import (
	"github.com/zzet/gortex/internal/graph"
)

// Release enrichment sidecar ("first appeared in <tag>").

var (
	_ graph.ReleaseEnrichmentWriter = (*Store)(nil)
	_ graph.ReleaseEnrichmentReader = (*Store)(nil)
)

func (s *Store) BulkSetReleases(repoPrefix string, rows []graph.ReleaseEnrichment) error {
	if s.refuseWrite("BulkSetReleases") { return ErrReadOnlyStore }
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
			`INSERT INTO release_enrichment (node_id, repo_prefix, added_in) VALUES ($1, $2, $3)
			 ON CONFLICT (node_id) DO UPDATE SET added_in = EXCLUDED.added_in`,
			r.NodeID, repoPrefix, r.AddedIn); err != nil {
			_ = tx.Rollback(s.ctx)
			return err
		}
	}
	return tx.Commit(s.ctx)
}

func (s *Store) DeleteReleases(nodeIDs []string) error {
	if s.refuseWrite("DeleteReleases") { return ErrReadOnlyStore }
	if len(nodeIDs) == 0 {
		return nil
	}
	_, err := s.pool.Exec(s.ctx, `DELETE FROM release_enrichment WHERE node_id = ANY($1)`, nodeIDs)
	return err
}

func (s *Store) ReleaseRows(repoPrefix string) []graph.ReleaseEnrichment {
	q := `SELECT node_id, repo_prefix, added_in FROM release_enrichment`
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

	var out []graph.ReleaseEnrichment
	for rows.Next() {
		var r graph.ReleaseEnrichment
		if err := rows.Scan(&r.NodeID, &r.RepoPrefix, &r.AddedIn); err != nil {
			return out
		}
		out = append(out, r)
	}
	return out
}
