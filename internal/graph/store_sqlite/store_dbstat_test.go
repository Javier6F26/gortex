package store_sqlite_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/graph/store_sqlite"
)

func TestDBStats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "g.sqlite")
	s, err := store_sqlite.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	// Force a write so the DB (and WAL, in WAL mode) carry real bytes.
	s.AddBatch([]*graph.Node{
		{ID: "a.go::Foo", Kind: graph.KindFunction, Name: "Foo", FilePath: "a.go"},
	}, nil)

	dbBytes, walBytes := s.DBStats()
	require.Greater(t, dbBytes, int64(0), "the on-disk DB file must have nonzero size")
	require.GreaterOrEqual(t, walBytes, int64(0), "WAL size is non-negative (0 after a checkpoint)")

	// A store with no path (the zero value) reports zero, never panics.
	var empty store_sqlite.Store
	db, wal := empty.DBStats()
	require.Equal(t, int64(0), db)
	require.Equal(t, int64(0), wal)
}
