package store_sqlite_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/graph/store_sqlite"
)

func openConstValStore(t *testing.T) *store_sqlite.Store {
	t.Helper()
	s, err := store_sqlite.Open(filepath.Join(t.TempDir(), "cv.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestConstantValues_Roundtrip(t *testing.T) {
	s := openConstValStore(t)
	rows := []graph.ConstantValueRow{
		{NodeID: "a.go::ChargeCardActivity", FilePath: "a.go", Value: "ChargeCard"},
		{NodeID: "a.go::RefundActivity", FilePath: "a.go", Value: "Refund"},
	}
	require.NoError(t, s.BulkSetConstantValues("repo", rows))

	got, err := s.ConstantValuesByNodeIDs([]string{"a.go::ChargeCardActivity", "a.go::RefundActivity", "missing"})
	require.NoError(t, err)
	assert.Equal(t, "ChargeCard", got["a.go::ChargeCardActivity"])
	assert.Equal(t, "Refund", got["a.go::RefundActivity"])
	_, ok := got["missing"]
	assert.False(t, ok)
}

func TestConstantValues_DeleteByFile(t *testing.T) {
	s := openConstValStore(t)
	require.NoError(t, s.BulkSetConstantValues("repo", []graph.ConstantValueRow{
		{NodeID: "a.go::X", FilePath: "a.go", Value: "vx"},
		{NodeID: "b.go::Y", FilePath: "b.go", Value: "vy"},
	}))
	require.NoError(t, s.DeleteConstantValuesByFiles("repo", []string{"a.go"}))

	got, err := s.ConstantValuesByNodeIDs([]string{"a.go::X", "b.go::Y"})
	require.NoError(t, err)
	_, gone := got["a.go::X"]
	assert.False(t, gone, "a.go's value must be deleted")
	assert.Equal(t, "vy", got["b.go::Y"], "b.go's value must remain")
}

func TestConstantValues_Replace(t *testing.T) {
	s := openConstValStore(t)
	require.NoError(t, s.BulkSetConstantValues("repo", []graph.ConstantValueRow{
		{NodeID: "a.go::X", FilePath: "a.go", Value: "old"},
	}))
	require.NoError(t, s.BulkSetConstantValues("repo", []graph.ConstantValueRow{
		{NodeID: "a.go::X", FilePath: "a.go", Value: "new"},
	}))
	got, err := s.ConstantValuesByNodeIDs([]string{"a.go::X"})
	require.NoError(t, err)
	assert.Equal(t, "new", got["a.go::X"], "INSERT OR REPLACE must update by node_id PK")
}

func TestConstantValues_EmptyNoop(t *testing.T) {
	s := openConstValStore(t)
	require.NoError(t, s.BulkSetConstantValues("repo", nil))
	require.NoError(t, s.DeleteConstantValuesByFiles("repo", nil))
	got, err := s.ConstantValuesByNodeIDs(nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}
