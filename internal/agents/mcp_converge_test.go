package agents

import "testing"

// TestMCPArgsConvergeOnMcp asserts the shared MCP entries emit the single
// canonical ["mcp"] args shape (no --index/--watch/--proxy).
func TestMCPArgsConvergeOnMcp(t *testing.T) {
	for name, entry := range map[string]map[string]any{
		"default": DefaultGortexMCPEntry(),
		"global":  GlobalGortexMCPEntry(),
	} {
		args, _ := entry["args"].([]string)
		if len(args) != 1 || args[0] != "mcp" {
			t.Errorf("%s entry args = %v, want [mcp]", name, args)
		}
	}
}

// TestUpsertMigratesLegacyShapes asserts the upsert rewrites every legacy
// on-disk args shape to the canonical ["mcp"] on first run, and is a no-op
// on the second run (idempotent, self-healing).
func TestUpsertMigratesLegacyShapes(t *testing.T) {
	legacy := map[string][]any{
		"index-watch": {"mcp", "--index", ".", "--watch"},
		"proxy":       {"mcp", "--proxy"},
	}
	for name, args := range legacy {
		t.Run(name, func(t *testing.T) {
			root := map[string]any{
				"mcpServers": map[string]any{
					"gortex": map[string]any{"command": "gortex", "args": args},
				},
			}
			if changed := UpsertMCPServerWithMigration(root, "gortex", DefaultGortexMCPEntry(), ApplyOpts{}); !changed {
				t.Fatal("first upsert should migrate a legacy stanza")
			}
			got := root["mcpServers"].(map[string]any)["gortex"].(map[string]any)["args"]
			gotArgs, _ := got.([]string)
			if len(gotArgs) != 1 || gotArgs[0] != "mcp" {
				t.Fatalf("migrated args = %v, want [mcp]", got)
			}
			if changed := UpsertMCPServerWithMigration(root, "gortex", DefaultGortexMCPEntry(), ApplyOpts{}); changed {
				t.Fatal("second upsert must be a no-op (already canonical)")
			}
		})
	}
}
