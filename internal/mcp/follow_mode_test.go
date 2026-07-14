package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/query"
)

// A follower forces the readonly/hide preset (overriding config), reports
// FollowMode(), and blocks mutating + external-side-effect tools while
// keeping read tools reachable.
func TestFollowMode_ForcesReadonlyPreset(t *testing.T) {
	g := graph.New()
	eng := query.NewEngine(g)
	srv := NewServer(eng, g, nil, nil, zap.NewNop(), nil, MultiRepoOptions{
		// A config preset that would normally widen the surface — follow
		// must override it.
		ToolPolicy: &ToolPolicyConfig{Preset: "full", Mode: "defer"},
		Follow:     true,
	})

	require.True(t, srv.FollowMode(), "FollowMode() should be true")
	require.NotNil(t, srv.toolPolicy)
	require.Equal(t, "readonly", srv.toolPolicy.preset, "follow forces the readonly preset")
	require.True(t, srv.toolPolicy.hideMode(), "follow forces hide mode")

	p := srv.toolPolicy
	// Read tools stay reachable.
	require.True(t, p.allows("search_symbols"), "read tool must be allowed")
	require.True(t, p.allows("get_symbol_source"), "read tool must be allowed")
	// Mutating / editor tools are blocked.
	require.False(t, p.allows("edit_file"), "editor tool must be blocked in follow mode")
	require.False(t, p.allows("index_repository"), "indexing tool must be blocked in follow mode")
	// Explicitly denied read-preset tools with external/FS side effects.
	require.False(t, p.allows("post_review"), "post_review must be denied in follow mode")
	require.False(t, p.allows("feedback"), "feedback must be denied in follow mode")
}

// The residual graph-writer point gates go inert under follow mode.
func TestFollowMode_ResidualWritersInert(t *testing.T) {
	g := graph.New()
	eng := query.NewEngine(g)
	srv := NewServer(eng, g, nil, nil, zap.NewNop(), nil, MultiRepoOptions{Follow: true})

	// ensureFresh must short-circuit (no MultiIndexer, read-only store).
	require.Nil(t, srv.ensureFresh([]string{"repoA/a.go"}), "ensureFresh must no-op in follow mode")

	// reconcileRationale must not write the rationale projection. With no
	// memory stores wired it is a no-op anyway; the guard makes it explicit
	// and must not panic.
	require.NotPanics(t, func() { srv.reconcileRationale("") })
}

// A non-follow server keeps the normal (non-forced) surface.
func TestFollowMode_OffKeepsNormalSurface(t *testing.T) {
	g := graph.New()
	eng := query.NewEngine(g)
	srv := NewServer(eng, g, nil, nil, zap.NewNop(), nil)
	require.False(t, srv.FollowMode())
}
