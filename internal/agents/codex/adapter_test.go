package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/zzet/gortex/internal/agents"
	"github.com/zzet/gortex/internal/agents/agentstest"
)

// TestCodexWritesMcpServersTOMLTable verifies we produce the
// documented [mcp_servers.gortex] table — not a legacy
// [mcp.gortex] or [mcpServers.gortex].
func TestCodexWritesMcpServersTOMLTable(t *testing.T) {
	env, _ := agentstest.NewEnv(t)
	// Detection sentinel: ~/.codex/ exists.
	if err := os.MkdirAll(filepath.Join(env.Home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := New()

	res, err := a.Apply(env, agents.ApplyOpts{})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	// Two creates: ~/.codex/config.toml for MCP plus AGENTS.md, the
	// per-repo instructions file Codex CLI reads on every task.
	agentstest.AssertCountsByAction(t, res, map[agents.ActionKind]int{agents.ActionCreate: 2})

	data, err := os.ReadFile(filepath.Join(env.Home, ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "mcp_servers") {
		t.Fatalf("expected mcp_servers table: %s", got)
	}
	if !strings.Contains(got, "gortex") {
		t.Fatalf("expected gortex entry: %s", got)
	}

	cfg := readCodexConfig(t, env)
	if count := gortexSessionStartHookCount(t, cfg); count != 1 {
		t.Fatalf("expected one Gortex SessionStart hook, got %d: %#v", count, cfg["hooks"])
	}

	agentstest.AssertIdempotent(t, a, env)
}

func TestCodexInstallsSessionStartHook(t *testing.T) {
	env := codexGlobalEnv(t)
	a := New()

	res, err := a.Apply(env, agents.ApplyOpts{})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	agentstest.AssertCountsByAction(t, res, map[agents.ActionKind]int{agents.ActionCreate: 1})

	cfg := readCodexConfig(t, env)
	entries := sessionStartEntries(t, cfg)
	if len(entries) != 1 {
		t.Fatalf("SessionStart entries=%d want 1: %#v", len(entries), entries)
	}
	entry := entries[0].(map[string]any)
	if entry["matcher"] != codexSessionStartMatcher {
		t.Fatalf("matcher=%v want %q", entry["matcher"], codexSessionStartMatcher)
	}
	handlers, ok := codexHookList(entry["hooks"])
	if !ok || len(handlers) != 1 {
		t.Fatalf("handlers=%#v", entry["hooks"])
	}
	handler := handlers[0].(map[string]any)
	if handler["type"] != "command" {
		t.Errorf("hook type=%v want command", handler["type"])
	}
	if handler["command"] != codexSessionStartCommand {
		t.Errorf("command=%v want %q", handler["command"], codexSessionStartCommand)
	}
	if handler["command_windows"] != codexSessionStartWindowsCommand {
		t.Errorf("command_windows=%v want %q", handler["command_windows"], codexSessionStartWindowsCommand)
	}
	command := handler["command"].(string)
	if !strings.Contains(command, "IMPORTANT: Prefer Gortex MCP tools") {
		t.Errorf("command should emit the graph-tools orientation: %v", handler["command"])
	}
	if !strings.Contains(command, "edit_file") {
		t.Errorf("command should mention edit_file: %v", handler["command"])
	}
	if strings.Contains(command, "[Gortex]") {
		t.Errorf("command should not duplicate the Gortex label: %v", handler["command"])
	}
}

func TestCodexSessionStartHookIdempotent(t *testing.T) {
	env := codexGlobalEnv(t)
	a := New()

	if _, err := a.Apply(env, agents.ApplyOpts{}); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	res, err := a.Apply(env, agents.ApplyOpts{})
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	agentstest.AssertCountsByAction(t, res, map[agents.ActionKind]int{agents.ActionSkip: 1})

	cfg := readCodexConfig(t, env)
	if count := gortexSessionStartHookCount(t, cfg); count != 1 {
		t.Fatalf("re-run duplicated Gortex SessionStart hook: got %d", count)
	}
}

func TestCodexSessionStartHookPreservesExistingConfig(t *testing.T) {
	env := codexGlobalEnv(t)
	path := codexConfigPath(env)
	seed := `model = "gpt-5-codex"

[mcp_servers.other]
command = "other"

[[hooks.SessionStart]]
matcher = "startup"

[[hooks.SessionStart.hooks]]
type = "command"
command = "echo user-session-start"
statusMessage = "User hook"
`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	a := New()
	res, err := a.Apply(env, agents.ApplyOpts{})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	agentstest.AssertCountsByAction(t, res, map[agents.ActionKind]int{agents.ActionMerge: 1})

	cfg := readCodexConfig(t, env)
	if cfg["model"] != "gpt-5-codex" {
		t.Fatalf("unrelated top-level key was clobbered: %#v", cfg)
	}
	servers := cfg["mcp_servers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Fatalf("existing MCP server was clobbered: %#v", servers)
	}
	if _, ok := servers["gortex"]; !ok {
		t.Fatalf("gortex MCP server missing after merge: %#v", servers)
	}
	entries := sessionStartEntries(t, cfg)
	if len(entries) != 2 {
		t.Fatalf("SessionStart entries=%d want user+gortex entries: %#v", len(entries), entries)
	}
	if !hasSessionStartCommand(t, cfg, "echo user-session-start") {
		t.Fatalf("user SessionStart hook was not preserved: %#v", entries)
	}
	if count := gortexSessionStartHookCount(t, cfg); count != 1 {
		t.Fatalf("Gortex SessionStart hooks=%d want 1", count)
	}
}

func TestCodexNoHooksSkipsSessionStartHook(t *testing.T) {
	env := codexGlobalEnv(t)
	env.InstallHooks = false
	a := New()

	if _, err := a.Apply(env, agents.ApplyOpts{}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	cfg := readCodexConfig(t, env)
	if _, ok := cfg["hooks"]; ok {
		t.Fatalf("--no-hooks should not write Codex hooks: %#v", cfg["hooks"])
	}
	if _, ok := cfg["mcp_servers"].(map[string]any)["gortex"]; !ok {
		t.Fatal("mcp_servers.gortex should still be written under --no-hooks")
	}

	plan, err := a.Plan(env)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Files) != 1 {
		t.Fatalf("plan files=%d want 1", len(plan.Files))
	}
	for _, key := range plan.Files[0].Keys {
		if key == "hooks" {
			t.Fatalf("Plan should not report hooks under --no-hooks: %#v", plan.Files[0].Keys)
		}
	}
}

func codexGlobalEnv(t *testing.T) agents.Env {
	t.Helper()
	env, _ := agentstest.NewEnv(t)
	env.Mode = agents.ModeGlobal
	if err := os.MkdirAll(filepath.Join(env.Home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	return env
}

func codexConfigPath(env agents.Env) string {
	return filepath.Join(env.Home, ".codex", "config.toml")
}

func readCodexConfig(t *testing.T, env agents.Env) map[string]any {
	t.Helper()
	data, err := os.ReadFile(codexConfigPath(env))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	var out map[string]any
	if err := toml.Unmarshal(data, &out); err != nil {
		t.Fatalf("parse config.toml: %v\n%s", err, data)
	}
	return out
}

func sessionStartEntries(t *testing.T, cfg map[string]any) []any {
	t.Helper()
	hooks, ok := cfg["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("missing hooks map: %#v", cfg)
	}
	entries, ok := codexHookList(hooks["SessionStart"])
	if !ok {
		t.Fatalf("hooks.SessionStart has unexpected shape: %#v", hooks["SessionStart"])
	}
	return entries
}

func gortexSessionStartHookCount(t *testing.T, cfg map[string]any) int {
	t.Helper()
	count := 0
	for _, entry := range sessionStartEntries(t, cfg) {
		if codexHookEntryIsGortexSessionStart(entry) {
			count++
		}
	}
	return count
}

func hasSessionStartCommand(t *testing.T, cfg map[string]any, command string) bool {
	t.Helper()
	for _, entry := range sessionStartEntries(t, cfg) {
		group, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		handlers, ok := codexHookList(group["hooks"])
		if !ok {
			continue
		}
		for _, handler := range handlers {
			hm, ok := handler.(map[string]any)
			if !ok {
				continue
			}
			if got, _ := hm["command"].(string); got == command {
				return true
			}
		}
	}
	return false
}
