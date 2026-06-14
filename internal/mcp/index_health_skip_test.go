package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

// TestIndexHealth_SkipRollupAndDensity verifies index_health rolls up
// synthetic skip nodes by reason and reports a nodes_per_file density.
func TestIndexHealth_SkipRollupAndDensity(t *testing.T) {
	srv, _ := setupTestServer(t)

	srv.graph.AddBatch([]*graph.Node{
		{ID: "skip_a.go", Kind: graph.KindFile, Name: "skip_a.go", FilePath: "skip_a.go", Meta: map[string]any{"skip_reason": "size"}},
		{ID: "skip_b.js", Kind: graph.KindFile, Name: "skip_b.js", FilePath: "skip_b.js", Meta: map[string]any{"skip_reason": "parse_failed"}},
		{ID: "skip_c.ts", Kind: graph.KindFile, Name: "skip_c.ts", FilePath: "skip_c.ts", Meta: map[string]any{"skip_reason": "parse_failed"}},
	}, nil)

	payload := srv.buildIndexHealthPayload()
	require.NotNil(t, payload)
	require.Contains(t, payload, "nodes_per_file")

	skipped, ok := payload["skipped"].(map[string]int)
	require.True(t, ok, "index_health must roll up skip_reason counts")
	require.Equal(t, 1, skipped["size"])
	require.Equal(t, 2, skipped["parse_failed"])
}
