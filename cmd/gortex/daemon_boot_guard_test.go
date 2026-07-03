package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/graph"
)

// TestSnapshotRoundTrip_RepoNodeEdgeCounts proves the additive per-repo
// NodeCount / EdgeCount fields survive a save + load cycle — the baseline the
// boot shape-degradation guard compares a reloaded repo against.
func TestSnapshotRoundTrip_RepoNodeEdgeCounts(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GORTEX_DAEMON_SNAPSHOT", filepath.Join(dir, "snap.gob.gz"))

	orig := graph.New()
	orig.AddNode(&graph.Node{ID: "r/a.go::Foo", Name: "Foo", Kind: graph.KindFunction, FilePath: "r/a.go"})

	repos := []snapshotRepo{{
		RepoPrefix: "r",
		RootPath:   "/tmp/r",
		FileMtimes: map[string]int64{"r/a.go": 123},
		NodeCount:  8956,
		EdgeCount:  58491,
	}}
	saveSnapshot(orig, repos, nil, snapshotVector{}, version, zap.NewNop())

	restored := graph.New()
	result, err := loadSnapshot(restored, zap.NewNop())
	require.NoError(t, err)
	require.True(t, result.Loaded)

	rec, ok := result.Repos["r"]
	require.True(t, ok, "repo record must round-trip keyed by prefix")
	assert.Equal(t, 8956, rec.NodeCount, "per-repo node count must round-trip")
	assert.Equal(t, 58491, rec.EdgeCount, "per-repo edge count must round-trip")
}

func TestBootShapeShortfall(t *testing.T) {
	// Baseline: a repo that saved 8956 nodes / 58491 edges.
	snap := map[string]*snapshotRepo{
		"r": {RepoPrefix: "r", NodeCount: 8956, EdgeCount: 58491},
		// A legacy / newly-tracked record with no recorded counts.
		"legacy": {RepoPrefix: "legacy", NodeCount: 0, EdgeCount: 0},
	}

	t.Run("edge collapse trips", func(t *testing.T) {
		// The measured degradation: 58491 -> 45026 edges is a 23% drop, which
		// does NOT trip the 50% floor — a modest shrink is tolerated.
		assert.False(t, bootShapeShortfall(snap, "r", 8512, 45026),
			"a 23% edge shrink is below the collapse floor")
		// A true collapse (edges more than halved) trips.
		assert.True(t, bootShapeShortfall(snap, "r", 8956, 20000),
			"edges more than halved must trip the guard")
	})

	t.Run("node collapse trips", func(t *testing.T) {
		assert.True(t, bootShapeShortfall(snap, "r", 3000, 58491),
			"nodes more than halved must trip the guard")
	})

	t.Run("healthy reload does not trip", func(t *testing.T) {
		assert.False(t, bootShapeShortfall(snap, "r", 8956, 58491),
			"an unchanged reload must not trip")
	})

	t.Run("zero baseline never trips", func(t *testing.T) {
		assert.False(t, bootShapeShortfall(snap, "legacy", 0, 0),
			"a record with no recorded counts has no baseline to compare")
	})

	t.Run("unknown prefix never trips", func(t *testing.T) {
		assert.False(t, bootShapeShortfall(snap, "absent", 1, 1),
			"a prefix absent from the snapshot has no baseline")
	})
}
