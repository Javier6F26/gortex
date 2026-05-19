package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExportContext_DoesNotBreakUnderGCXSession is the regression
// guard for the parse-failure path that this commit fixes.
//
// export_context delegates to handleSmartContext for the raw data
// and then json.Unmarshal's it. Before the fix, the inner call
// inherited the session's client-aware format default — for known
// clients like claude-code that resolves to GCX1, which made the
// unmarshal fail with "invalid character 'G'". The fix forces
// `format: "json"` on the inner call so the unmarshal sees JSON
// regardless of session or outer format.
func TestExportContext_DoesNotBreakUnderGCXSession(t *testing.T) {
	srv, _ := setupTestServer(t)
	// Bind a session with a client name that maps to GCX, so the
	// session-resolved format would otherwise leak into the inner
	// smart_context call.
	srv.NoteSessionClient("session_export", "claude-code", "1.0.42")
	ctx := WithSessionID(context.Background(), "session_export")

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"task": "validate the export_context wiring",
	}

	res, err := srv.handleExportContext(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.False(t, res.IsError,
		"export_context must not return an error result under a GCX-resolving session; got: %+v",
		res.Content)

	require.NotEmpty(t, res.Content)
	tc, ok := res.Content[0].(mcp.TextContent)
	require.True(t, ok)
	assert.Contains(t, tc.Text, "# Context Briefing",
		"markdown header should be present in the default render")
}

// TestExportContext_OuterFormatJSONStillWorks verifies the JSON
// output path still produces structured data (format=json on the
// outer call), independent of the inner GCX-forcing patch.
func TestExportContext_OuterFormatJSONStillWorks(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.NoteSessionClient("session_export_json", "claude-code", "1.0.42")
	ctx := WithSessionID(context.Background(), "session_export_json")

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"task":   "validate the export_context json path",
		"format": "json",
	}

	res, err := srv.handleExportContext(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.False(t, res.IsError)

	tc := res.Content[0].(mcp.TextContent)
	// JSON output: should start with `{` not `# Context Briefing`.
	assert.True(t, strings.HasPrefix(strings.TrimSpace(tc.Text), "{"),
		"format=json must produce JSON, got: %q", tc.Text)
}

// TestExportContext_OuterRequestArgsUnmutated guards against the
// inner-format override leaking back into the caller's arg map.
// The handler clones args before mutating; a shared-map regression
// would corrupt the outer request and confuse downstream telemetry.
func TestExportContext_OuterRequestArgsUnmutated(t *testing.T) {
	srv, _ := setupTestServer(t)
	ctx := context.Background()

	outerArgs := map[string]any{
		"task":   "argument-isolation check",
		"format": "markdown",
	}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = outerArgs

	_, err := srv.handleExportContext(ctx, req)
	require.NoError(t, err)

	// The caller-provided map should still say "markdown" — the
	// handler's clone-then-override must not have written through.
	assert.Equal(t, "markdown", outerArgs["format"],
		"outer args mutated by the inner-format override")
}
