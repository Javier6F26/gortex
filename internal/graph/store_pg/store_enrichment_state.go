package store_pg

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/zzet/gortex/internal/graph"
)

// Enrichment state persistence for deferred enrichment gating.

var _ graph.EnrichmentStateStore = (*Store)(nil)

func (s *Store) GetEnrichmentState(repoPrefix, provider string) (graph.EnrichmentState, bool, error) {
	var st graph.EnrichmentState
	err := s.pool.QueryRow(s.ctx,
		`SELECT repo_prefix, provider, indexed_sha, completed_at, coverage
		 FROM enrichment_state WHERE repo_prefix = $1 AND provider = $2`,
		repoPrefix, provider).Scan(&st.RepoPrefix, &st.Provider, &st.IndexedSHA, &st.CompletedAt, &st.Coverage)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return st, false, nil
		}
		return st, false, err
	}
	return st, true, nil
}

func (s *Store) SetEnrichmentState(state graph.EnrichmentState) error {
	_, err := s.pool.Exec(s.ctx,
		`INSERT INTO enrichment_state (repo_prefix, provider, indexed_sha, completed_at, coverage)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (repo_prefix, provider) DO UPDATE SET
		   indexed_sha = EXCLUDED.indexed_sha, completed_at = EXCLUDED.completed_at, coverage = EXCLUDED.coverage`,
		state.RepoPrefix, state.Provider, state.IndexedSHA, state.CompletedAt, state.Coverage)
	return err
}
