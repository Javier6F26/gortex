package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/daemon"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/indexer"
	gortexmcp "github.com/zzet/gortex/internal/mcp"
	"github.com/zzet/gortex/internal/parser"
	"github.com/zzet/gortex/internal/parser/languages"
	"github.com/zzet/gortex/internal/query"

	"github.com/zzet/gortex/internal/config"
)

// trackedPathMCPSetup builds a minimal Server + MultiIndexer with one
// repo tracked at `root`. Used by the not-tracked-guard tests so we can
// drive the dispatcher end-to-end without spinning up a real daemon.
func trackedPathMCPSetup(t *testing.T, root string) (*mcpDispatcher, *indexer.MultiIndexer) {
	t.Helper()
	g := graph.New()
	reg := parser.NewRegistry()
	languages.RegisterAll(reg)

	cm, err := config.NewConfigManager("")
	require.NoError(t, err)

	idx := indexer.New(g, reg, config.Default().Index, zap.NewNop())
	mi := indexer.NewMultiIndexer(g, reg, idx.Search(), cm, zap.NewNop())

	// Register a repo the dispatcher will recognize. We bypass indexing
	// by stuffing metadata directly — the isCWDTracked check only reads
	// AllMetadata.
	if _, err := mi.TrackRepoCtx(context.Background(), config.RepoEntry{
		Path: root,
	}); err != nil {
		t.Fatalf("track test repo: %v", err)
	}

	eng := query.NewEngine(g)
	srv := gortexmcp.NewServer(eng, g, idx, nil, zap.NewNop(), nil, gortexmcp.MultiRepoOptions{
		MultiIndexer:  mi,
		ConfigManager: cm,
	})

	return newMCPDispatcher(srv, mi, zap.NewNop()), mi
}

func TestDispatcher_UntrackedCWD_ReturnsStructuredError(t *testing.T) {
	// Tracked root is a directory the test creates; untracked is a
	// sibling path the dispatcher shouldn't know about.
	tracked := t.TempDir()
	untracked := t.TempDir()

	d, _ := trackedPathMCPSetup(t, tracked)

	sess := &daemon.Session{ID: "sess_x", CWD: untracked}
	frame := []byte(`{"jsonrpc":"2.0","id":7,"method":"graph_stats","params":{}}`)

	reply, err := d.Dispatch(context.Background(), sess, frame)
	require.NoError(t, err)
	require.NotNil(t, reply, "untracked cwd must produce a reply, not silence")

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(reply, &parsed))

	errObj, ok := parsed["error"].(map[string]any)
	require.True(t, ok, "response must carry an error object: %v", parsed)

	// Machine-readable data for tool UIs.
	data, ok := errObj["data"].(map[string]any)
	require.True(t, ok, "error.data must be present for client-side handling")
	assert.Equal(t, "repo_not_tracked", data["error_code"])
	assert.Equal(t, untracked, data["path"])
	assert.Contains(t, data["suggestion"], "gortex track")

	// The response id must echo the inbound id so the client can pair
	// it with the in-flight request.
	assert.EqualValues(t, 7, parsed["id"])
}

func TestDispatcher_TrackedCWD_Passes(t *testing.T) {
	tracked := t.TempDir()
	d, _ := trackedPathMCPSetup(t, tracked)

	sess := &daemon.Session{ID: "sess_y", CWD: tracked}
	// The method string doesn't matter for this test — we're proving
	// the dispatcher passes the frame through to MCPServer instead of
	// short-circuiting on the tracked-cwd guard. Whatever mcp-go does
	// with the method (including "method not found" for bogus ones) is
	// evidence that our guard let it through.
	frame := []byte(`{"jsonrpc":"2.0","id":1,"method":"graph_stats","params":{}}`)

	reply, err := d.Dispatch(context.Background(), sess, frame)
	require.NoError(t, err)
	require.NotNil(t, reply)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(reply, &parsed))

	// The response may carry an mcp-go protocol error (method name isn't
	// the right shape — real tool calls go through tools/call), but it
	// must NOT carry OUR "repo_not_tracked" sentinel.
	if errObj, ok := parsed["error"].(map[string]any); ok {
		if data, ok := errObj["data"].(map[string]any); ok {
			assert.NotEqual(t, "repo_not_tracked", data["error_code"],
				"tracked cwd wrongly rejected by guard: %v", parsed)
		}
	}
}

func TestDispatcher_SubdirectoryOfTrackedRoot_Passes(t *testing.T) {
	tracked := t.TempDir()
	// A nested path inside a tracked root also counts as tracked — an
	// agent opened in repo/internal/auth is still "in" the repo.
	nested := filepath.Join(tracked, "internal", "auth")

	d, _ := trackedPathMCPSetup(t, tracked)

	assert.True(t, d.isCWDTracked(nested),
		"subdirectory of tracked root must be recognized as tracked")
	assert.True(t, d.isCWDTracked(tracked),
		"tracked root itself must be recognized as tracked")
	assert.False(t, d.isCWDTracked(filepath.Dir(tracked)),
		"parent of tracked root must NOT be recognized as tracked")
}

func TestDispatcher_NilMultiIndexer_AllowsEverything(t *testing.T) {
	// Single-repo mode has no multi-indexer. The guard must not reject
	// in that case — otherwise we'd break the embedded stdio path.
	d := newMCPDispatcher(nil, nil, zap.NewNop())
	assert.True(t, d.isCWDTracked("/anywhere"))
	assert.True(t, d.isCWDTracked(""))
}
