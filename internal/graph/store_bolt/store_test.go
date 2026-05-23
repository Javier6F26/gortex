package store_bolt_test

import (
	"path/filepath"
	"testing"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/graph/store_bolt"
	"github.com/zzet/gortex/internal/graph/storetest"
)

// TestBoltStoreConformance runs the cross-backend conformance suite
// against the bbolt-backed store. Each subtest gets its own temp DB so
// state cannot leak between runs.
func TestBoltStoreConformance(t *testing.T) {
	storetest.RunConformance(t, func(t *testing.T) graph.Store {
		dir := t.TempDir()
		s, err := store_bolt.Open(filepath.Join(dir, "test.db"))
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}
