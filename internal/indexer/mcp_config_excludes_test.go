package indexer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
	"github.com/zzet/gortex/internal/parser/languages"
)

// TestIndex_MCPConfigUnderAgentDirs verifies the MCP-config-as-graph
// feature reaches its documented .kiro/.claude targets: those dirs are
// Builtin-excluded wholesale, so the indexer must descend them just far
// enough to index MCP server configs — while keeping the surrounding
// agent-state noise (settings.json, skills) out of the graph.
func TestIndex_MCPConfigUnderAgentDirs(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, content string) {
		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		writeFile(t, p, content)
	}
	// Noise that must stay OUT of the graph.
	mustWrite(".claude/settings.json", `{"theme": "dark"}`)
	// MCP configs that MUST be indexed despite living under excluded dirs.
	mustWrite(".claude/mcp.json", `{"mcpServers": {"claudeone": {"command": "npx", "args": ["-y", "some-pkg"]}}}`)
	mustWrite(".kiro/settings/mcp.json", `{"mcpServers": {"kiroone": {"command": "uvx", "args": ["other-pkg"]}}}`)

	reg := parser.NewRegistry()
	reg.Register(languages.NewMCPConfigExtractor())
	reg.Register(languages.NewJSONExtractor()) // settings.json WOULD index if not excluded
	cfg := config.Default().Index
	cfg.Workers = 2

	g := graph.New()
	idx := New(g, reg, cfg, zap.NewNop())
	_, err := idx.Index(dir)
	require.NoError(t, err)

	if findKindNamed(g, graph.KindResource, "claudeone") == nil {
		t.Error("MCP server 'claudeone' from .claude/mcp.json was not indexed")
	}
	if findKindNamed(g, graph.KindResource, "kiroone") == nil {
		t.Error("MCP server 'kiroone' from .kiro/settings/mcp.json was not indexed")
	}

	// The agent-state noise must NOT have leaked into the graph.
	for _, n := range g.AllNodes() {
		if strings.Contains(n.FilePath, "settings.json") {
			t.Errorf("agent-state noise leaked into graph: %s (%s)", n.FilePath, n.Kind)
		}
	}
}
