package mcp

import (
	"encoding/json"
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

type searchTextMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

func TestSearchText(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"),
		[]byte("package app\n\nfunc Alpha() {}\n\nfunc Beta() { Alpha() }\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "other.go"),
		[]byte("package app\n\nfunc Gamma() {}\n"), 0o644))

	g := graph.New()
	reg := parser.NewRegistry()
	languages.RegisterAll(reg)
	cfg := config.Default()
	idx := indexer.New(g, reg, cfg.Index, zap.NewNop())
	_, err := idx.Index(dir)
	require.NoError(t, err)
	eng := query.NewEngine(g)
	srv := NewServer(eng, g, idx, nil, zap.NewNop(), nil)

	decode := func(res *mcplib.CallToolResult) struct {
		Matches []searchTextMatch `json:"matches"`
		Count   int               `json:"count"`
	} {
		require.False(t, res.IsError)
		var out struct {
			Matches []searchTextMatch `json:"matches"`
			Count   int               `json:"count"`
		}
		require.NoError(t, json.Unmarshal([]byte(res.Content[0].(mcplib.TextContent).Text), &out))
		return out
	}

	// A literal present in exactly one file.
	resp := decode(callTool(t, srv, "search_text", map[string]any{"query": "func Beta"}))
	require.Equal(t, 1, resp.Count)
	require.Equal(t, "main.go", resp.Matches[0].Path)
	require.Equal(t, 5, resp.Matches[0].Line)

	// A literal present nowhere.
	none := decode(callTool(t, srv, "search_text", map[string]any{"query": "zzz_absent_literal"}))
	require.Equal(t, 0, none.Count)

	// The limit argument caps the result set.
	limited := decode(callTool(t, srv, "search_text",
		map[string]any{"query": "package app", "limit": 1}))
	require.Equal(t, 1, limited.Count)

	// An empty query is a tool error.
	bad := callTool(t, srv, "search_text", map[string]any{})
	require.True(t, bad.IsError)
}

// TestSearchText_EnclosingSymbol confirms each literal hit is
// decorated with the graph symbol that encloses the matching line.
func TestSearchText_EnclosingSymbol(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"),
		[]byte("package app\n\nfunc Alpha() {\n\tprintln(\"needle_here\")\n}\n"), 0o644))

	g := graph.New()
	reg := parser.NewRegistry()
	languages.RegisterAll(reg)
	cfg := config.Default()
	idx := indexer.New(g, reg, cfg.Index, zap.NewNop())
	_, err := idx.Index(dir)
	require.NoError(t, err)
	eng := query.NewEngine(g)
	srv := NewServer(eng, g, idx, nil, zap.NewNop(), nil)

	res := callTool(t, srv, "search_text", map[string]any{"query": "needle_here"})
	require.False(t, res.IsError)
	var out struct {
		Matches []struct {
			Path       string `json:"path"`
			Line       int    `json:"line"`
			SymbolID   string `json:"symbol_id"`
			SymbolName string `json:"symbol_name"`
		} `json:"matches"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].(mcplib.TextContent).Text), &out))
	require.Len(t, out.Matches, 1)
	require.Equal(t, "Alpha", out.Matches[0].SymbolName,
		"the literal lands inside func Alpha -- search_text should report it as the enclosing symbol")
	require.NotEmpty(t, out.Matches[0].SymbolID)
}

// TestSearchText_PathScoping confirms the path argument confines the
// literal-search hits to the named sub-path.
func TestSearchText_PathScoping(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "services", "billing"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "services", "auth"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "services", "billing", "b.go"),
		[]byte("package billing\n\n// shared_marker here\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "services", "auth", "a.go"),
		[]byte("package auth\n\n// shared_marker here\n"), 0o644))

	g := graph.New()
	reg := parser.NewRegistry()
	languages.RegisterAll(reg)
	cfg := config.Default()
	idx := indexer.New(g, reg, cfg.Index, zap.NewNop())
	_, err := idx.Index(dir)
	require.NoError(t, err)
	eng := query.NewEngine(g)
	srv := NewServer(eng, g, idx, nil, zap.NewNop(), nil)

	// Without a path filter both files match.
	all := callTool(t, srv, "search_text", map[string]any{"query": "shared_marker"})
	require.False(t, all.IsError)
	var allOut struct {
		Matches []struct {
			Path string `json:"path"`
		} `json:"matches"`
	}
	require.NoError(t, json.Unmarshal([]byte(all.Content[0].(mcplib.TextContent).Text), &allOut))
	require.Len(t, allOut.Matches, 2)

	// With a path filter only the billing file matches.
	scoped := callTool(t, srv, "search_text",
		map[string]any{"query": "shared_marker", "path": "services/billing"})
	require.False(t, scoped.IsError)
	var scopedOut struct {
		Matches []struct {
			Path string `json:"path"`
		} `json:"matches"`
	}
	require.NoError(t, json.Unmarshal([]byte(scoped.Content[0].(mcplib.TextContent).Text), &scopedOut))
	require.Len(t, scopedOut.Matches, 1)
	require.Contains(t, scopedOut.Matches[0].Path, "services/billing")
}
