package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveSymbolID_RepoRelative asserts a session working inside a repo
// can pass a repo-relative symbol id and have it resolved to the canonical
// prefixed id, while a full id is returned unchanged and an unknown id is a
// no-op.
func TestResolveSymbolID_RepoRelative(t *testing.T) {
	srv, repoA, _ := newIsolationServer(t)
	ctx := sessionCtx("s1", repoA)

	// Canonical (prefixed) id of a symbol indexed in repoA.
	var fullID string
	for _, n := range srv.graph.AllNodes() {
		if n.Name == "AlphaThing" {
			fullID = n.ID
			break
		}
	}
	require.NotEmpty(t, fullID, "AlphaThing should be indexed in repoA")

	_, _, prefix, ok := srv.multiIndexer.ScopeForCWD(repoA)
	require.True(t, ok)
	require.NotEmpty(t, prefix)
	relative := strings.TrimPrefix(fullID, prefix+"/")
	require.NotEqual(t, fullID, relative, "the canonical id must carry the repo prefix")

	// Repo-relative id (no prefix) → resolves to the canonical prefixed id.
	assert.Equal(t, fullID, srv.resolveSymbolID(ctx, relative))
	// A full id is returned unchanged (exact match first).
	assert.Equal(t, fullID, srv.resolveSymbolID(ctx, fullID))
	// An unknown / non-symbol id is a no-op.
	assert.Equal(t, "nope::Missing", srv.resolveSymbolID(ctx, "nope::Missing"))
	// With no session cwd, only the exact form resolves (relative stays put).
	assert.Equal(t, relative, srv.resolveSymbolID(context.Background(), relative))
}
