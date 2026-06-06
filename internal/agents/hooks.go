package agents

import (
	"slices"
	"strings"
)

// hooks.go installs Gortex lifecycle hooks into the Gemini-CLI /
// Antigravity settings.json. These agents share Claude Code's
// hookSpecificOutput.additionalContext wire shape, so a gortex hook can
// inject the same session orientation (SessionStart) and stale-index
// hints (AfterTool) it gives Claude — closing the gap where the
// non-Claude adapters wrote MCP config but no lifecycle hooks.

// GortexHookName is the identifier stamped on every hook entry Gortex
// installs, so a re-run detects and skips (or, with Force, replaces)
// its own entries without disturbing the user's other hooks.
const GortexHookName = "gortex"

// geminiHookTimeoutMS is the per-hook timeout. Gemini CLI measures hook
// timeouts in milliseconds (Claude Code uses seconds), so this is 10s.
const geminiHookTimeoutMS = 10000

// UpsertGeminiHooks installs Gortex SessionStart + AfterTool lifecycle
// hooks into a Gemini-CLI / Antigravity settings.json root, politely
// merging with any existing user hooks (existing entries are preserved;
// gortex's own entry is added once and is idempotent on re-run).
// agentFlag is the value passed to `gortex hook --agent <agentFlag>`.
// Returns true when the root was modified.
func UpsertGeminiHooks(root map[string]any, agentFlag string, opts ApplyOpts) (changed bool) {
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		hooks = map[string]any{}
	}
	cmd := "gortex hook --agent " + agentFlag

	// SessionStart: full session orientation (no tool matcher).
	if upsertHookEvent(hooks, "SessionStart", "", cmd, "Gortex session orientation", opts) {
		changed = true
	}
	// AfterTool: a concise stale-index hint after the tools where the
	// index can drift (shell, search, glob).
	if upsertHookEvent(hooks, "AfterTool", "run_shell_command|search_file_content|glob", cmd, "Gortex graph context + stale-index hint", opts) {
		changed = true
	}

	if changed {
		root["hooks"] = hooks
	}
	return changed
}

// upsertHookEvent appends a gortex hook group to one event's array,
// preserving existing user entries. When a gortex entry is already
// present it is a no-op (idempotent) unless opts.Force, which replaces
// it. Returns true when the array was modified.
func upsertHookEvent(hooks map[string]any, event, matcher, cmd, desc string, opts ApplyOpts) bool {
	arr, _ := hooks[event].([]any)

	hasGortex := slices.ContainsFunc(arr, hookGroupIsGortex)
	if hasGortex && !opts.Force {
		return false
	}
	if hasGortex && opts.Force {
		// Drop existing gortex groups; keep the user's.
		kept := make([]any, 0, len(arr))
		for _, e := range arr {
			if !hookGroupIsGortex(e) {
				kept = append(kept, e)
			}
		}
		arr = kept
	}

	inner := map[string]any{
		"type":        "command",
		"command":     cmd,
		"name":        GortexHookName,
		"timeout":     geminiHookTimeoutMS,
		"description": desc,
	}
	group := map[string]any{"hooks": []any{inner}}
	if matcher != "" {
		group["matcher"] = matcher
	}
	hooks[event] = append(arr, group)
	return true
}

// hookGroupIsGortex reports whether a hook group was installed by
// Gortex — either tagged with GortexHookName or carrying a
// `gortex hook` command.
func hookGroupIsGortex(e any) bool {
	g, ok := e.(map[string]any)
	if !ok {
		return false
	}
	inner, ok := g["hooks"].([]any)
	if !ok {
		return false
	}
	for _, h := range inner {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := hm["name"].(string); name == GortexHookName {
			return true
		}
		if cmd, _ := hm["command"].(string); strings.Contains(cmd, "gortex hook") {
			return true
		}
	}
	return false
}
