package store_sqlite_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/graph/store_sqlite"
)

func openIndexStateStore(t *testing.T) *store_sqlite.Store {
	t.Helper()
	s, err := store_sqlite.Open(filepath.Join(t.TempDir(), "is.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRepoIndexState_RoundTrip(t *testing.T) {
	s := openIndexStateStore(t)

	// Absent state reads back as (zero, false, nil).
	got, ok, err := s.GetRepoIndexState("gortex")
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, "gortex", got.RepoPrefix)

	want := graph.RepoIndexState{
		RepoPrefix:        "gortex",
		IndexedSHA:        "abc123",
		Dirty:             true,
		IndexedAt:         1700000000,
		WorkspaceFP:       "deadbeef",
		NodeCount:         42,
		EdgeCount:         99,
		ExtractorVersions: `{"go":2}`,
	}
	require.NoError(t, s.SetRepoIndexState(want))

	got, ok, err = s.GetRepoIndexState("gortex")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, want, got)

	// Upsert replaces in place (one row per repo_prefix).
	want.IndexedSHA = "def456"
	want.Dirty = false
	require.NoError(t, s.SetRepoIndexState(want))
	got, ok, err = s.GetRepoIndexState("gortex")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "def456", got.IndexedSHA)
	require.False(t, got.Dirty)

	// A different repo is isolated.
	_, ok, err = s.GetRepoIndexState("other")
	require.NoError(t, err)
	require.False(t, ok)
}
