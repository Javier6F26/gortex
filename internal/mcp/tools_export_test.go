package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

// TestExportGraph_InlineFormats asserts export_graph returns the rendered
// export inline (no output_path) for each format.
func TestExportGraph_InlineFormats(t *testing.T) {
	srv, _ := setupTestServer(t)
	for _, format := range []string{"cypher", "graphml", "mermaid"} {
		req := mcp.CallToolRequest{}
		req.Params.Arguments = map[string]any{"format": format}
		res, err := srv.handleExportGraph(context.Background(), req)
		require.NoError(t, err)
		require.False(t, res.IsError, "format %s should not error: %v", format, res.Content)
		tc, ok := res.Content[0].(mcp.TextContent)
		require.True(t, ok, "format %s should return text content", format)
		require.NotEmpty(t, tc.Text, "format %s should produce output", format)
	}
}

// TestExportGraph_WritesFile asserts export_graph writes to an absolute
// output_path and reports it (the daemon owns the filesystem write).
func TestExportGraph_WritesFile(t *testing.T) {
	srv, _ := setupTestServer(t)
	out := filepath.Join(t.TempDir(), "graph.cypher")
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"format": "cypher", "output_path": out}
	res, err := srv.handleExportGraph(context.Background(), req)
	require.NoError(t, err)
	require.False(t, res.IsError)
	if _, statErr := os.Stat(out); statErr != nil {
		t.Fatalf("export_graph did not write the file: %v", statErr)
	}
	tc, ok := res.Content[0].(mcp.TextContent)
	require.True(t, ok)
	require.True(t, strings.Contains(tc.Text, "output_path"), "summary should name the output_path: %s", tc.Text)
}

// TestExportGraph_UnknownFormat asserts an unknown format is a tool error.
func TestExportGraph_UnknownFormat(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"format": "neo4j-binary"}
	res, err := srv.handleExportGraph(context.Background(), req)
	require.NoError(t, err)
	require.True(t, res.IsError, "unknown format must be a tool error")
}
