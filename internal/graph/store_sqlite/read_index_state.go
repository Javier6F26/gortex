package store_sqlite

import (
	"database/sql"
	"fmt"
	"os"

	"github.com/zzet/gortex/internal/graph"
)

// ReadRepoIndexStates opens the SQLite store at path read-only and returns
// every repo_index_state freshness row keyed by repo_prefix.
//
// It is a deliberately lightweight side door for read-only callers (notably
// `gortex repos`) that must inspect index freshness WITHOUT going through
// Open — which runs schema migrations, alters columns, starts a checkpoint
// goroutine, and (on a version mismatch) can refuse to open or rebuild the
// file. None of that is appropriate for a status command that may run while
// a daemon holds the same store open.
//
// The connection is query-only and inherits the database's existing journal
// mode, so it reads safely alongside a running daemon (WAL permits concurrent
// readers). A missing store file, or a database that predates the
// repo_index_state table, both yield an empty map and a nil error — that is
// "nothing recorded yet", not a failure, so the caller can fall back to other
// freshness sources rather than surfacing an error to the user.
func ReadRepoIndexStates(path string) (map[string]graph.RepoIndexState, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return map[string]graph.RepoIndexState{}, nil
		}
		return nil, fmt.Errorf("stat sqlite store %q: %w", path, err)
	}

	// query_only blocks accidental writes; busy_timeout keeps a brief read
	// from erroring out if the daemon happens to hold the write lock for a
	// moment. We deliberately do NOT set journal_mode — forcing it could try
	// to switch the live database's mode; inheriting the on-disk WAL mode is
	// exactly what a concurrent reader wants.
	dsn := path + "?_pragma=busy_timeout(2000)&_pragma=query_only(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite store %q: %w", path, err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)

	rows, err := db.Query(`
SELECT repo_prefix, indexed_sha, dirty, indexed_at, workspace_fp, node_count, edge_count, extractor_versions
  FROM repo_index_state`)
	if err != nil {
		// A store written before the repo_index_state table existed (or any
		// other read error) is treated as "no freshness recorded yet" — a
		// status command must never hard-fail on a degraded cache.
		return map[string]graph.RepoIndexState{}, nil
	}
	defer func() { _ = rows.Close() }()

	out := map[string]graph.RepoIndexState{}
	for rows.Next() {
		var st graph.RepoIndexState
		var dirty int
		if err := rows.Scan(&st.RepoPrefix, &st.IndexedSHA, &dirty, &st.IndexedAt,
			&st.WorkspaceFP, &st.NodeCount, &st.EdgeCount, &st.ExtractorVersions); err != nil {
			return nil, fmt.Errorf("scan repo_index_state: %w", err)
		}
		st.Dirty = dirty != 0
		out[st.RepoPrefix] = st
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate repo_index_state: %w", err)
	}
	return out, nil
}
