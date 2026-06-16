package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestToolCategory(t *testing.T) {
	cases := map[string]string{
		// prefix-driven
		"find_files":           toolCatNav,
		"search_symbols":       toolCatNav,
		"edit_file":            toolCatEdit,
		"write_file":           toolCatEdit,
		"rename_symbol":        toolCatEdit,
		"overlay_push":         toolCatOverlay,
		"subscribe_diagnostics": toolCatSubscription,
		"unsubscribe_diagnostics": toolCatSubscription,
		"enrich_churn":         toolCatEnrich,
		"notebook_save":        toolCatMemory,
		// override-driven (prefix would mislabel)
		"edit_memory":          toolCatMemory,
		"rename_memory":        toolCatMemory,
		"smart_context":        toolCatNav,
		"read_file":            toolCatRead,
		"get_symbol_source":    toolCatRead,
		"analyze":              toolCatAnalysis,
		"review":               toolCatReview,
		"pr_risk":              toolCatPR,
		"list_repos":           toolCatWorkspace,
		"graph_stats":          toolCatAdmin,
		"tool_profile":         toolCatAdmin,
		// unclassified
		"some_unknown_future_tool": toolCatOther,
	}
	for name, want := range cases {
		require.Equalf(t, want, toolCategory(name), "tool %q", name)
	}
}

func TestToolCategories_Map(t *testing.T) {
	got := toolCategories([]string{"edit_file", "search_symbols", "analyze"})
	require.Equal(t, map[string]string{
		"edit_file":      toolCatEdit,
		"search_symbols": toolCatNav,
		"analyze":        toolCatAnalysis,
	}, got)
}
