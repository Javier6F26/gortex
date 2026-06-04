package excludes

import "testing"

func TestInAgentConfigDir(t *testing.T) {
	in := []string{".claude", ".claude/", ".claude/mcp.json", ".claude/skills/x.md", ".kiro", ".kiro/settings/mcp.json"}
	out := []string{"", ".", "src/main.go", "claude/x", ".cursor/mcp.json", "a/.claude/mcp.json"}
	for _, p := range in {
		if !InAgentConfigDir(p) {
			t.Errorf("InAgentConfigDir(%q) = false, want true", p)
		}
	}
	for _, p := range out {
		if InAgentConfigDir(p) {
			t.Errorf("InAgentConfigDir(%q) = true, want false", p)
		}
	}
}

func TestIsMCPConfigFile(t *testing.T) {
	yes := []string{"mcp.json", ".mcp.json", ".cursor/mcp.json", ".kiro/settings/mcp.json", "claude_desktop_config.json", "foo.mcp.json"}
	no := []string{"settings.json", "package.json", "mcp.yaml", "x.md", "mcp.json.bak"}
	for _, p := range yes {
		if !IsMCPConfigFile(p) {
			t.Errorf("IsMCPConfigFile(%q) = false, want true", p)
		}
	}
	for _, p := range no {
		if IsMCPConfigFile(p) {
			t.Errorf("IsMCPConfigFile(%q) = true, want false", p)
		}
	}
}
