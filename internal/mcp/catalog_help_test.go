package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

// TestAnalyzeHelp_RoutesThroughHandler proves kind:"help" is dispatched to
// the catalogue response (not the scope/graph path) end to end.
func TestAnalyzeHelp_RoutesThroughHandler(t *testing.T) {
	srv := setupPresetServer(t, ToolPolicyConfig{Preset: "full"})
	req := mcp.CallToolRequest{}
	req.Params.Name = "analyze"
	req.Params.Arguments = map[string]any{"kind": "help"}
	res, err := srv.handleAnalyze(context.Background(), req)
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Contains(t, toolResultText(res), "dead_code:")
}

func TestSearchASTHelp_RoutesThroughHandler(t *testing.T) {
	srv := setupPresetServer(t, ToolPolicyConfig{Preset: "full"})
	req := mcp.CallToolRequest{}
	req.Params.Name = "search_ast"
	req.Params.Arguments = map[string]any{"detector": "help"}
	res, err := srv.handleSearchAST(context.Background(), req)
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Contains(t, toolResultText(res), "error-not-wrapped")
}

func TestAnalyzeHelp_ReturnsFullCatalog(t *testing.T) {
	// Every analyze kind's one-liner is present in kind:"help", so an agent
	// without MCP resource support still gets the full reference the diet
	// pulled out of the tool description.
	txt := analyzeHelpResult()
	require.Contains(t, txt, "dead_code:")
	require.Contains(t, txt, "hotspots:")
	require.Contains(t, txt, "cross_repo:")
	for k := range analyzeKindDescriptions {
		require.Containsf(t, txt, "- "+k+":", "kind %q missing from analyze help catalogue", k)
	}
}

func TestSearchASTHelp_ReturnsFullDetectorCatalog(t *testing.T) {
	txt := searchASTHelpResult()
	// A flagship bundled detector and the SAST family are both present.
	require.Contains(t, txt, "error-not-wrapped")
	require.Contains(t, txt, "detectors —")
	require.Greater(t, strings.Count(txt, "\n- "), 50,
		"detector help must enumerate the whole rule library")
}

func TestAnalyzeDescription_IsLeanButComplete(t *testing.T) {
	// The description no longer inlines the per-kind catalogue, but still
	// tells the agent how to get it and names the kind families.
	require.Less(t, len(analyzeGroupedSummary), 2000,
		"analyze description must stay lean after the catalogue moved out")
	require.Contains(t, analyzeGroupedSummary, "kind:\"help\"")
	require.Contains(t, analyzeGroupedSummary, "gortex://schema")
	require.Contains(t, analyzeGroupedSummary, "security")
}

func TestSchemaResource_CarriesBothCatalogs(t *testing.T) {
	srv := setupPresetServer(t, ToolPolicyConfig{Preset: "full"})
	req := mcp.ReadResourceRequest{}
	req.Params.URI = "gortex://schema"
	contents, err := srv.handleResourceSchema(context.Background(), req)
	require.NoError(t, err)
	require.NotEmpty(t, contents)
	trc, ok := contents[0].(mcp.TextResourceContents)
	require.True(t, ok)
	require.Contains(t, trc.Text, "## analyze kinds")
	require.Contains(t, trc.Text, "## search_ast detectors")
	require.Contains(t, trc.Text, "dead_code:")
	require.Contains(t, trc.Text, "error-not-wrapped")
}
