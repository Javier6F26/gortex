package mcp

import (
	"context"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

func TestPlanningMode_TogglesAndBlocksEdits(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()

	setMode := func(mode string) *mcplib.CallToolResult {
		req := mcplib.CallToolRequest{}
		req.Params.Name = "set_planning_mode"
		req.Params.Arguments = map[string]any{"mode": mode}
		res, err := srv.handleSetPlanningMode(ctx, req)
		require.NoError(t, err)
		return res
	}

	// Enter planning mode.
	require.False(t, setMode("planning").IsError, "set_planning_mode planning must succeed")

	// The tools/list filter now hides editing tools but keeps read tools.
	filtered := srv.toolSurfaceFilter(ctx, []mcplib.Tool{
		{Name: "edit_file"}, {Name: "write_file"}, {Name: "search_symbols"},
	})
	visible := map[string]bool{}
	for _, tl := range filtered {
		visible[tl.Name] = true
	}
	require.False(t, visible["edit_file"], "edit_file must be filtered out in planning mode")
	require.False(t, visible["write_file"], "write_file must be filtered out in planning mode")
	require.True(t, visible["search_symbols"], "read tools stay visible in planning mode")

	// An editing tool is hard-blocked even when its handler is called
	// directly through the wrapped dispatch path.
	ef := srv.MCPServer().GetTool("edit_file")
	require.NotNil(t, ef, "edit_file must be a live tool")
	editReq := mcplib.CallToolRequest{}
	editReq.Params.Name = "edit_file"
	editReq.Params.Arguments = map[string]any{
		"path": "main.go", "old_string": "hello", "new_string": "hi",
	}
	res, err := ef.Handler(ctx, editReq)
	require.NoError(t, err)
	require.True(t, res.IsError, "an editing tool must be blocked in planning mode")
	require.Contains(t, res.Content[0].(mcplib.TextContent).Text, "tool_blocked_by_mode")

	// Back to editing mode — the gate lifts.
	require.False(t, setMode("editing").IsError)
	require.Len(t, srv.toolSurfaceFilter(ctx, []mcplib.Tool{{Name: "edit_file"}}), 1,
		"edit_file returns to the surface in editing mode")

	res, err = ef.Handler(ctx, editReq)
	require.NoError(t, err)
	require.NotContains(t, res.Content[0].(mcplib.TextContent).Text, "tool_blocked_by_mode",
		"editing tools must not be mode-blocked once editing mode is restored")
}

func TestPlanningMode_RejectsUnknownMode(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := mcplib.CallToolRequest{}
	req.Params.Name = "set_planning_mode"
	req.Params.Arguments = map[string]any{"mode": "banana"}
	res, err := srv.handleSetPlanningMode(context.Background(), req)
	require.NoError(t, err)
	require.True(t, res.IsError, "an unknown mode must be rejected")
}
