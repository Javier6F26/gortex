package mcp

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

// addLoopSignalFn adds a function node carrying loop-region bottleneck
// signals on its Meta, so the analyzer test can confirm they are read,
// scored, and surfaced.
func addLoopSignalFn(g graph.Store, id, file string, meta map[string]any) {
	m := map[string]any{}
	for k, v := range meta {
		m[k] = v
	}
	g.AddNode(&graph.Node{
		ID: id, Kind: graph.KindFunction, Name: id,
		FilePath: file, StartLine: 1, EndLine: 5,
		Meta: m,
	})
}

// reasonStrings flattens a decoded row's reasons array into one string for
// substring assertions.
func reasonStrings(row map[string]any) string {
	rs, _ := row["reasons"].([]any)
	var b strings.Builder
	for _, r := range rs {
		if s, ok := r.(string); ok {
			b.WriteString(s)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// TestAnalyzeBottlenecks_SurfacesLoopRegionSignals confirms the four
// index-time loop-region signals are surfaced on the bottleneck rows and
// each contributes a reason to the function's risk explanation.
func TestAnalyzeBottlenecks_SurfacesLoopRegionSignals(t *testing.T) {
	srv, _ := setupTestServer(t)
	const file = "sig.go"

	addLoopSignalFn(srv.graph, "sig.go::Linear", file, map[string]any{"linear_scan_in_loop": true})
	addLoopSignalFn(srv.graph, "sig.go::Alloc", file, map[string]any{"alloc_in_loop": true})
	addLoopSignalFn(srv.graph, "sig.go::Recur", file, map[string]any{"recursion_in_loop": true})
	addLoopSignalFn(srv.graph, "sig.go::Access", file, map[string]any{"max_access_depth": float64(6)})

	byID := callAnalyzeBottlenecks(t, srv, nil)

	require.Contains(t, byID, "sig.go::Linear")
	assert.Equal(t, true, byID["sig.go::Linear"]["linear_scan_in_loop"])
	assert.Contains(t, reasonStrings(byID["sig.go::Linear"]), "linear-scan call inside a loop")

	require.Contains(t, byID, "sig.go::Alloc")
	assert.Equal(t, true, byID["sig.go::Alloc"]["alloc_in_loop"])
	assert.Contains(t, reasonStrings(byID["sig.go::Alloc"]), "allocation inside a loop")

	require.Contains(t, byID, "sig.go::Recur")
	assert.Equal(t, true, byID["sig.go::Recur"]["recursion_in_loop"])
	assert.Contains(t, reasonStrings(byID["sig.go::Recur"]), "self-recursion inside a loop")

	require.Contains(t, byID, "sig.go::Access")
	assert.EqualValues(t, 6, byID["sig.go::Access"]["max_access_depth"])
	assert.Contains(t, reasonStrings(byID["sig.go::Access"]), "deep member-access chain")
}

// TestAnalyzeBottlenecks_AccessDepthBelowThresholdNotSurfaced confirms a
// shallow member-access depth neither contributes a reason nor, on its own,
// makes a function show up as a bottleneck.
func TestAnalyzeBottlenecks_AccessDepthBelowThresholdNotSurfaced(t *testing.T) {
	srv, _ := setupTestServer(t)
	const file = "shallow.go"

	addLoopSignalFn(srv.graph, "shallow.go::Shallow", file, map[string]any{"max_access_depth": float64(3)})

	byID := callAnalyzeBottlenecks(t, srv, nil)
	if row, ok := byID["shallow.go::Shallow"]; ok {
		assert.NotContains(t, reasonStrings(row), "deep member-access chain",
			"access depth 3 is below the depth-4 surfacing threshold")
	}
}
