package mcp

import (
	"context"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

func callWorkflow(t *testing.T, srv *Server, ctx context.Context, args map[string]any) *mcplib.CallToolResult {
	t.Helper()
	req := mcplib.CallToolRequest{}
	req.Params.Name = "workflow"
	req.Params.Arguments = args
	res, err := srv.handleWorkflow(ctx, req)
	require.NoError(t, err)
	return res
}

func editFileReq() mcplib.CallToolRequest {
	r := mcplib.CallToolRequest{}
	r.Params.Name = "edit_file"
	r.Params.Arguments = map[string]any{"path": "main.go", "old_string": "hello", "new_string": "hi"}
	return r
}

func TestWorkflow_BlockModeGatesEditsByPhase(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()

	require.False(t, callWorkflow(t, srv, ctx, map[string]any{"action": "start", "mode": "block"}).IsError)

	ef := srv.MCPServer().GetTool("edit_file")
	require.NotNil(t, ef)

	// An edit in the explore phase is refused with a structured error.
	res, err := ef.Handler(ctx, editFileReq())
	require.NoError(t, err)
	require.True(t, res.IsError, "edits must be blocked in the explore phase")
	require.Contains(t, res.Content[0].(mcplib.TextContent).Text, "tool_out_of_phase")

	// Advance to the implement phase — edits are permitted.
	require.False(t, callWorkflow(t, srv, ctx, map[string]any{"action": "advance"}).IsError)
	res, err = ef.Handler(ctx, editFileReq())
	require.NoError(t, err)
	require.NotContains(t, res.Content[0].(mcplib.TextContent).Text, "tool_out_of_phase",
		"edits must be allowed once the workflow reaches the implement phase")

	require.False(t, callWorkflow(t, srv, ctx, map[string]any{"action": "stop"}).IsError)
}

func TestWorkflow_WarnModeAutoAdvances(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()
	require.False(t, callWorkflow(t, srv, ctx, map[string]any{"action": "start", "mode": "warn"}).IsError)

	// In warn mode an out-of-phase edit auto-advances instead of failing.
	ef := srv.MCPServer().GetTool("edit_file")
	res, err := ef.Handler(ctx, editFileReq())
	require.NoError(t, err)
	require.NotContains(t, res.Content[0].(mcplib.TextContent).Text, "tool_out_of_phase",
		"warn mode must auto-advance instead of blocking")
}

func TestWorkflow_SurfaceFilterHidesEditsInExplorePhase(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()
	require.False(t, callWorkflow(t, srv, ctx, map[string]any{"action": "start", "mode": "block"}).IsError)

	filtered := srv.toolSurfaceFilter(ctx, []mcplib.Tool{{Name: "edit_file"}, {Name: "search_symbols"}})
	visible := map[string]bool{}
	for _, tl := range filtered {
		visible[tl.Name] = true
	}
	require.False(t, visible["edit_file"], "edit_file is hidden in the explore phase of a block-mode workflow")
	require.True(t, visible["search_symbols"], "read tools stay visible")
}

func TestWorkflow_AdvanceWithoutStartErrors(t *testing.T) {
	srv, _ := setupTestServer(t)
	res := callWorkflow(t, srv, context.Background(), map[string]any{"action": "advance"})
	require.True(t, res.IsError, "advancing with no active workflow must error")
}
