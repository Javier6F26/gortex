package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

func TestEditFileExpectedOccurrences(t *testing.T) {
	srv, dir := setupTestServer(t)
	p := filepath.Join(dir, "occ.txt")
	require.NoError(t, os.WriteFile(p, []byte("x\nx\nx\n"), 0o644))

	// Mismatch: 3 occurrences but the caller asserted 2 -> refuse, no write.
	req := mcplib.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"path": p, "old_string": "x", "new_string": "y",
		"replace_all": true, "expected_occurrences": float64(2),
	}
	res, err := srv.handleEditFile(context.Background(), req)
	require.NoError(t, err)
	require.True(t, res.IsError, "a cardinality mismatch must refuse the edit")
	after, _ := os.ReadFile(p)
	require.Equal(t, "x\nx\nx\n", string(after), "a refused edit must not write")

	// Match: asserted 3 -> applies.
	resp := callEditHandlerJSON(t, srv.handleEditFile, map[string]any{
		"path": p, "old_string": "x", "new_string": "y",
		"replace_all": true, "expected_occurrences": float64(3),
	})
	require.Equal(t, "applied", resp["status"])
	after, _ = os.ReadFile(p)
	require.Equal(t, "y\ny\ny\n", string(after))
}
