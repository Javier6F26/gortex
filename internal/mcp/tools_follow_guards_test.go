package mcp

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/graph"
)

// newFollowGuardServer builds a minimal follow-mode server. The disk /
// working-tree guards under test return before touching the engine, so a
// bare graph + follow flag is enough to exercise them without a Postgres
// backend.
func newFollowGuardServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{
		graph:      graph.New(),
		logger:     zap.NewNop(),
		session:    newSessionState(),
		sessions:   newSessionMap(),
		tokenStats: &tokenStats{},
		symHistory: &symbolHistory{entries: make(map[string][]SymbolModification)},
		toolScopes: newScopeRegistry(),
	}
	s.SetFollowMode(true)
	require.True(t, s.FollowMode())
	return s
}

func requireFollowNoDisk(t *testing.T, res *mcp.CallToolResult, tool string) {
	t.Helper()
	require.NotNil(t, res)
	require.True(t, res.IsError, "expected a follow_no_disk error")
	tc, ok := res.Content[0].(mcp.TextContent)
	require.True(t, ok)
	require.Contains(t, tc.Text, "follow_no_disk")
	require.Contains(t, tc.Text, tool)
}

// sibling_diff_context must route through the follow_no_disk guard like
// diff_context — not leak a raw "could not resolve a repository root" (4.3).
func TestFollowGuard_SiblingDiffContext(t *testing.T) {
	s := newFollowGuardServer(t)
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{}
	res, err := s.handleSiblingDiffContext(context.Background(), req)
	require.NoError(t, err)
	requireFollowNoDisk(t, res, "sibling_diff_context")
}

// audit_agent_config must return follow_no_disk on a diskless follower,
// not a clean-looking files_scanned: 0 (4.4).
func TestFollowGuard_AuditAgentConfig(t *testing.T) {
	s := newFollowGuardServer(t)
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{}
	res, err := s.handleAuditAgentConfig(context.Background(), req)
	require.NoError(t, err)
	requireFollowNoDisk(t, res, "audit_agent_config")
}

// generate_wiki writes pages to disk; the write seal must reject it with
// follow_no_disk before any mkdir, so the OS permission error is never the
// backstop (4.6).
func TestFollowGuard_GenerateWiki(t *testing.T) {
	s := newFollowGuardServer(t)
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{}
	res, err := s.handleGenerateWiki(context.Background(), req)
	require.NoError(t, err)
	requireFollowNoDisk(t, res, "generate_wiki")
}

// find_co_changing_symbols on a follower (which never mines co-change from
// git) must NOT claim "mining_in_progress / retry shortly" — that promise
// is false and was inconsistent (only the first call saw it). It reports a
// stable persisted-edges-only unavailability note instead (4.5).
func TestFollowGuard_FindCoChangingSymbols_NoFalseMiningPromise(t *testing.T) {
	s := newFollowGuardServer(t)
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"file_path": "repoA/nowhere.go"}

	// Two calls must answer consistently.
	for i := 0; i < 2; i++ {
		res, err := s.handleFindCoChangingSymbols(context.Background(), req)
		require.NoError(t, err)
		require.NotNil(t, res)
		require.False(t, res.IsError)
		tc := res.Content[0].(mcp.TextContent)
		require.NotContains(t, tc.Text, "mining_in_progress", "a follower never mines; must not claim it does")
		require.NotContains(t, tc.Text, "retry shortly", "must not promise a retry that will never help")
		require.Contains(t, tc.Text, "persisted_edges_only")
	}
}
