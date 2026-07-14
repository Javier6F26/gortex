package store_pg

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/zzet/gortex/internal/graph"
)

// Index state read/write for per-repo freshness tracking.

var (
	_ graph.RepoIndexStateWriter = (*Store)(nil)
	_ graph.RepoIndexStateReader = (*Store)(nil)
)

// NewestRepoIndexedAt returns the maximum indexed_at (unix seconds) across
// every repo in the schema and whether any row exists. Followers use it to
// report freshness lag without needing a repo roster. It is a read and
// routes through the retry/degrade path.
func (s *Store) NewestRepoIndexedAt() (int64, bool) {
	var newest int64
	var has bool
	s.withReadRetry("NewestRepoIndexedAt", func() error {
		var maxAt *int64
		if err := s.pool.QueryRow(s.ctx, `SELECT MAX(indexed_at) FROM repo_index_state`).Scan(&maxAt); err != nil {
			return err
		}
		if maxAt != nil {
			newest = *maxAt
			has = true
		}
		return nil
	})
	return newest, has
}

// GetRepoIndexState reads the index state for a repo prefix.
func (s *Store) GetRepoIndexState(repoPrefix string) (graph.RepoIndexState, bool, error) {
	if repoPrefix == "" {
		return graph.RepoIndexState{}, false, nil
	}
	var st graph.RepoIndexState
	var dirty int64
	err := s.pool.QueryRow(s.ctx,
		`SELECT repo_prefix, indexed_sha, dirty, indexed_at, workspace_fp, node_count, edge_count, extractor_versions
		 FROM repo_index_state WHERE repo_prefix = $1`, repoPrefix).
		Scan(&st.RepoPrefix, &st.IndexedSHA, &dirty, &st.IndexedAt,
			&st.WorkspaceFP, &st.NodeCount, &st.EdgeCount, &st.ExtractorVersions)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return graph.RepoIndexState{}, false, nil
		}
		return graph.RepoIndexState{}, false, err
	}
	st.Dirty = dirty != 0
	return st, true, nil
}

// SetRepoIndexState upserts the index state for a repo prefix.
func (s *Store) SetRepoIndexState(st graph.RepoIndexState) error {
	if s.refuseWrite("SetRepoIndexState") { return ErrReadOnlyStore }
	dirty := int64(0)
	if st.Dirty {
		dirty = 1
	}
	_, err := s.pool.Exec(s.ctx,
		`INSERT INTO repo_index_state (repo_prefix, indexed_sha, dirty, indexed_at, workspace_fp, node_count, edge_count, extractor_versions)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (repo_prefix) DO UPDATE SET
		   indexed_sha = EXCLUDED.indexed_sha, dirty = EXCLUDED.dirty, indexed_at = EXCLUDED.indexed_at,
		   workspace_fp = EXCLUDED.workspace_fp, node_count = EXCLUDED.node_count,
		   edge_count = EXCLUDED.edge_count, extractor_versions = EXCLUDED.extractor_versions`,
		st.RepoPrefix, st.IndexedSHA, dirty, st.IndexedAt,
		st.WorkspaceFP, st.NodeCount, st.EdgeCount, st.ExtractorVersions)
	return err
}

// AllRepoIndexStates reads every repo index state.
func (s *Store) AllRepoIndexStates() ([]graph.RepoIndexState, error) {
	rows, err := s.pool.Query(s.ctx,
		`SELECT repo_prefix, indexed_sha, dirty, indexed_at, workspace_fp, node_count, edge_count, extractor_versions
		 FROM repo_index_state ORDER BY repo_prefix`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []graph.RepoIndexState
	for rows.Next() {
		var st graph.RepoIndexState
		var dirty int64
		if err := rows.Scan(&st.RepoPrefix, &st.IndexedSHA, &dirty, &st.IndexedAt,
			&st.WorkspaceFP, &st.NodeCount, &st.EdgeCount, &st.ExtractorVersions); err != nil {
			return out, err
		}
		st.Dirty = dirty != 0
		out = append(out, st)
	}
	return out, rows.Err()
}
