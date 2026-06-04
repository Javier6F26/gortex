package excludes

import (
	"path"
	"path/filepath"
	"strings"
)

// agentConfigRoots are editor/agent state directories that Builtin
// excludes wholesale (their bulk content — settings, transcripts, skills —
// is noise) but which may also carry an MCP server config the
// MCP-config-as-graph feature wants in the graph. Callers descend these
// subtrees and index only the MCP config files within them; see the
// indexer's shouldExclude.
var agentConfigRoots = []string{".claude", ".kiro"}

// mcpConfigExactBasenames and mcpConfigSuffix mirror the MCP config
// extractor's Extensions() (internal/parser/languages/mcp_config.go) —
// keep them in sync. ".mcp.json" is matched as a suffix (so a repo-root
// ".mcp.json" or "x.mcp.json" qualifies); the rest are exact basenames.
var mcpConfigExactBasenames = map[string]bool{
	"mcp.json":                   true,
	"claude_desktop_config.json": true,
}

const mcpConfigSuffix = ".mcp.json"

// InAgentConfigDir reports whether a repo-root-relative path is the root
// of, or lives under, one of the agent-config directories (.claude/,
// .kiro/). Used to carve a descend-but-only-MCP-configs exception out of
// the wholesale Builtin exclusion of those directories.
func InAgentConfigDir(relPath string) bool {
	rel := strings.TrimPrefix(filepath.ToSlash(relPath), "./")
	for _, root := range agentConfigRoots {
		if rel == root || strings.HasPrefix(rel, root+"/") {
			return true
		}
	}
	return false
}

// IsMCPConfigFile reports whether a path's basename is a recognised MCP
// server config file name.
func IsMCPConfigFile(relPath string) bool {
	base := path.Base(filepath.ToSlash(relPath))
	if mcpConfigExactBasenames[base] {
		return true
	}
	return strings.HasSuffix(base, mcpConfigSuffix)
}
