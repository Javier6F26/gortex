package mcp

import (
	"os"
	"path/filepath"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/indexer"
	"github.com/zzet/gortex/internal/parser"
	"github.com/zzet/gortex/internal/parser/languages"
	"github.com/zzet/gortex/internal/query"
)

// TestSymbolNotFound_DidYouMean pins the recovery hint on a missed symbol
// id: a stale/typo'd name surfaces the nearest symbol in the same file, a
// right-name/wrong-path id surfaces where the symbol actually lives, and a
// genuinely unknown id in an empty path still errors cleanly with no hint.
func TestSymbolNotFound_DidYouMean(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"),
		[]byte("package app\n\nfunc AlphaHandler() {}\n\nfunc BetaHandler() {}\n"), 0o644))

	g := graph.New()
	reg := parser.NewRegistry()
	languages.RegisterAll(reg)
	cfg := config.Default()
	idx := indexer.New(g, reg, cfg.Index, zap.NewNop())
	_, err := idx.Index(dir)
	require.NoError(t, err)
	eng := query.NewEngine(g)
	srv := NewServer(eng, g, idx, nil, zap.NewNop(), nil)

	errText := func(res *mcplib.CallToolResult) string {
		require.True(t, res.IsError)
		return res.Content[0].(mcplib.TextContent).Text
	}

	// Typo'd name in the right file → nearest symbol in that file.
	typo := errText(callTool(t, srv, "get_symbol_source", map[string]any{"id": "main.go::AlphaHandlr"}))
	require.Contains(t, typo, "symbol not found")
	require.Contains(t, typo, "did you mean")
	require.Contains(t, typo, "AlphaHandler")

	// Right name, wrong path → the id where the symbol actually lives.
	wrongPath := errText(callTool(t, srv, "get_symbol_source", map[string]any{"id": "nope.go::BetaHandler"}))
	require.Contains(t, wrongPath, "BetaHandler")

	// edit_symbol shares the hint.
	editMiss := errText(callTool(t, srv, "edit_symbol", map[string]any{
		"id": "main.go::AlphaHandlr", "old_source": "a", "new_source": "b",
	}))
	require.Contains(t, editMiss, "did you mean")

	// Unknown name in a path with no indexed symbols → clean error, no hint.
	clean := errText(callTool(t, srv, "get_symbol_source", map[string]any{"id": "ghost.go::Nonexistent"}))
	require.Contains(t, clean, "symbol not found")
	require.NotContains(t, clean, "did you mean")
}
