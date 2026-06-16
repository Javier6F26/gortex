package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	gortexmcp "github.com/zzet/gortex/internal/mcp"
)

func editSurface(t *testing.T) *gortexmcp.ToolSurface {
	t.Helper()
	// Explicit allow list — exactly these tools (plus always-kept).
	return gortexmcp.NewToolSurface(
		gortexmcp.ToolPolicyConfig{Allow: []string{"search_symbols", "edit_file"}}, zap.NewNop())
}

func TestFilterToolsListFrame(t *testing.T) {
	surface := editSurface(t)
	require.True(t, surface.Active())

	frame := []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[` +
		`{"name":"search_symbols","description":"s"},` +
		`{"name":"analyze","description":"a"},` +
		`{"name":"edit_file","description":"e"},` +
		`{"name":"tool_profile","description":"t"}],"nextCursor":"abc"}}` + "\n")

	out := filterToolsListFrame(frame, surface)

	var msg struct {
		ID     int `json:"id"`
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(out, &msg))

	var names []string
	for _, tl := range msg.Result.Tools {
		names = append(names, tl.Name)
	}
	require.ElementsMatch(t, []string{"search_symbols", "edit_file", "tool_profile"}, names,
		"analyze must be dropped; always-kept tool_profile must survive")
	require.Equal(t, 1, msg.ID, "id preserved")
	require.Equal(t, "abc", msg.Result.NextCursor, "other result fields preserved")
}

func TestFilterToolsListFrame_PassThrough(t *testing.T) {
	surface := editSurface(t)

	// A non-tools/list result (a tools/call result) is untouched.
	callResult := []byte(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"x"}]}}` + "\n")
	require.Equal(t, callResult, filterToolsListFrame(callResult, surface))

	// An inactive surface never rewrites.
	inactive := gortexmcp.NewToolSurface(gortexmcp.ToolPolicyConfig{}, zap.NewNop())
	require.False(t, inactive.Active())
	toolsList := []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"analyze"}]}}` + "\n")
	require.Equal(t, toolsList, filterToolsListFrame(toolsList, inactive))
}

func TestGateToolCallFrame(t *testing.T) {
	surface := editSurface(t)

	// A call to a forbidden tool is gated with a JSON-RPC error reply.
	blocked := []byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"analyze","arguments":{}}}` + "\n")
	reply, gated := gateToolCallFrame(blocked, surface)
	require.True(t, gated)
	var errMsg struct {
		ID    int `json:"id"`
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(reply, &errMsg))
	require.Equal(t, 7, errMsg.ID)
	require.Equal(t, -32601, errMsg.Error.Code)
	require.Contains(t, errMsg.Error.Message, "analyze")

	// A call to an allowed tool is forwarded (not gated).
	allowed := []byte(`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"edit_file"}}` + "\n")
	_, gated = gateToolCallFrame(allowed, surface)
	require.False(t, gated)

	// Non-tools/call frames pass through.
	listReq := []byte(`{"jsonrpc":"2.0","id":9,"method":"tools/list"}` + "\n")
	_, gated = gateToolCallFrame(listReq, surface)
	require.False(t, gated)

	// An inactive surface gates nothing.
	inactive := gortexmcp.NewToolSurface(gortexmcp.ToolPolicyConfig{}, zap.NewNop())
	_, gated = gateToolCallFrame(blocked, inactive)
	require.False(t, gated)
}

func TestToolPolicyConfigFromFlags(t *testing.T) {
	cfg := toolPolicyConfigFromFlags("search_symbols,edit_file", "hide")
	require.Equal(t, "", cfg.Preset)
	require.Equal(t, []string{"search_symbols", "edit_file"}, cfg.Allow)
	require.Equal(t, "hide", cfg.Mode)

	cfg = toolPolicyConfigFromFlags("edit,+find_files", "")
	require.Equal(t, "edit", cfg.Preset)
	require.Equal(t, []string{"find_files"}, cfg.Allow)
}
