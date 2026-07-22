package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

// requireFollowAnalysisDisabled asserts the result is the honest
// analysis-disabled response for a follower: it explains analysis does not
// run here and never suggests index_repository (impossible on a follower).
func requireFollowAnalysisDisabled(t *testing.T, res *mcp.CallToolResult) {
	t.Helper()
	require.NotNil(t, res)
	require.NotEmpty(t, res.Content)
	tc, ok := res.Content[0].(mcp.TextContent)
	require.True(t, ok)
	require.Contains(t, tc.Text, "analysis does not run on this follower")
	require.NotContains(t, tc.Text, "index_repository")
}

// The heavy on-demand analysis tools must short-circuit on a follower rather
// than materialise the corpus (the store_pg backend implements neither
// PageRanker nor CommunityDetector, so these fall through to in-process
// full-graph scans). We assert the honest response; the guard returns before
// any scan, so a bare graph is enough.
func TestFollowAnalysisGate_OnDemandToolsShortCircuit(t *testing.T) {
	cases := []struct {
		name string
		call func(s *Server, ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)
	}{
		{"analyze clusters", (*Server).handleAnalyzeClusters},
		{"analyze pagerank", (*Server).handleAnalyzePageRank},
		{"analyze louvain", (*Server).handleAnalyzeLouvain},
		{"find hotspots", (*Server).handleFindHotspots},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newFollowGuardServer(t)
			req := mcp.CallToolRequest{}
			req.Params.Arguments = map[string]any{}
			res, err := tc.call(s, context.Background(), req)
			require.NoError(t, err)
			requireFollowAnalysisDisabled(t, res)
		})
	}
}

// The cached-nil analysis readers must swap their misleading
// "run index_repository first" hint for the honest follower message.
func TestFollowAnalysisGate_ReadersHonestMessage(t *testing.T) {
	cases := []struct {
		name string
		call func(s *Server, ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)
	}{
		{"get_communities", (*Server).handleGetCommunities},
		{"get_processes", (*Server).handleGetProcesses},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newFollowGuardServer(t)
			req := mcp.CallToolRequest{}
			req.Params.Arguments = map[string]any{}
			res, err := tc.call(s, context.Background(), req)
			require.NoError(t, err)
			require.NotNil(t, res)
			tc0, ok := res.Content[0].(mcp.TextContent)
			require.True(t, ok)
			require.False(t, strings.Contains(tc0.Text, "index_repository"),
				"follower reader must not suggest index_repository")
			require.Contains(t, tc0.Text, "follower")
		})
	}
}

// incrementalCommunities is the on-demand self-heal entry point
// (get_cluster / analyze clusters). On a follower it must return nil without
// running DetectCommunitiesLeidenIncremental, so a query can never trigger
// the full-graph scan the boot pass was removed to avoid.
func TestFollowAnalysisGate_IncrementalCommunitiesNoScan(t *testing.T) {
	s := newFollowGuardServer(t)
	cr, stats := s.incrementalCommunities()
	require.Nil(t, cr, "follower incrementalCommunities must not compute a partition")
	require.False(t, stats.Incremental, "no incremental pass should be reported")
}

// A writer (follow mode off) keeps the legacy message so only followers see
// the new wording.
func TestFollowAnalysisGate_WriterKeepsLegacyMessage(t *testing.T) {
	s := newFollowGuardServer(t)
	s.SetFollowMode(false)
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{}
	res, err := s.handleGetCommunities(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, res)
	tc0, ok := res.Content[0].(mcp.TextContent)
	require.True(t, ok)
	require.Contains(t, tc0.Text, "index_repository")
}
