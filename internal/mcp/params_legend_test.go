package mcp

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

func mkTool(props map[string]any) mcp.Tool {
	return mcp.Tool{
		Name:        "t",
		Description: "d",
		InputSchema: mcp.ToolInputSchema{Type: "object", Properties: props},
	}
}

func desc(t *testing.T, tool mcp.Tool, param string) string {
	t.Helper()
	pm, ok := tool.InputSchema.Properties[param].(map[string]any)
	require.True(t, ok, "param %q missing", param)
	s, _ := pm["description"].(string)
	return s
}

func TestCompactSharedToolParams_RewritesWireParams(t *testing.T) {
	tool := mkTool(map[string]any{
		"format":    map[string]any{"type": "string", "description": "Output format: json (default), gcx (GCX1 compact wire format), or toon"},
		"max_bytes": map[string]any{"type": "number", "description": "Cap the marshaled response at this many bytes. The longest list is trimmed; truncation metadata rides on the response. Omit for no cap."},
		"repo":      map[string]any{"type": "string", "description": "Repository prefix or path (multi-repo mode); defaults to the lone tracked repo or the session's cwd-bound repo"},
	})
	compactSharedToolParams(&tool)
	require.Contains(t, desc(t, tool, "format"), "see server instructions")
	require.Contains(t, desc(t, tool, "max_bytes"), "see server instructions")
	require.Contains(t, desc(t, tool, "repo"), "see server instructions")
	// The verbose paragraphs are gone.
	require.NotContains(t, desc(t, tool, "max_bytes"), "longest list is trimmed")
}

func TestCompactSharedToolParams_LeavesBespokeParamsAlone(t *testing.T) {
	// A `scope` that means diff-scope, and a `cursor` that means the nav
	// focus — same names, different semantics — must NOT be rewritten.
	tool := mkTool(map[string]any{
		"scope":  map[string]any{"type": "string", "description": "unstaged (default), staged, all, or compare"},
		"cursor": map[string]any{"type": "string", "description": "(register/heartbeat) Symbol ID or file the agent is currently focused on."},
	})
	compactSharedToolParams(&tool)
	require.Equal(t, "unstaged (default), staged, all, or compare", desc(t, tool, "scope"))
	require.Contains(t, desc(t, tool, "cursor"), "currently focused")
}

func TestCompactSharedToolParams_NeverInflates(t *testing.T) {
	// An already-terse format hint shorter than the shared gloss is kept.
	tool := mkTool(map[string]any{
		"format": map[string]any{"type": "string", "description": "json|gcx|toon"},
	})
	compactSharedToolParams(&tool)
	require.Equal(t, "json|gcx|toon", desc(t, tool, "format"))
}

func TestSharedParamLegend_InServerInstructions(t *testing.T) {
	require.True(t, strings.Contains(serverInstructions, sharedParamLegend),
		"the shared-parameter legend must ship in the server instructions")
	require.Contains(t, sharedParamLegend, "max_bytes")
	require.Contains(t, sharedParamLegend, "format")
}
