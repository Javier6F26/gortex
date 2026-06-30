// Package codex implements the Gortex init integration for the
// OpenAI Codex CLI. Codex stores MCP server definitions in a TOML
// file — ~/.codex/config.toml for the default scope — under the
// [mcp_servers.<name>] table:
//
//	[mcp_servers.gortex]
//	command = "gortex"
//	args = ["mcp", "--index", ".", "--watch"]
//	[mcp_servers.gortex.env]
//	GORTEX_INDEX_WORKERS = "8"
//
// Docs: https://github.com/openai/codex/blob/main/docs/config.md
package codex

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/zzet/gortex/internal/agents"
	"github.com/zzet/gortex/internal/agents/internalutil"
)

const Name = "codex"
const DocsURL = "https://developers.openai.com/codex/mcp"

const codexSessionStartMatcher = "startup|resume|clear|compact"
const codexSessionStartMessage = "IMPORTANT: Prefer Gortex MCP tools (search_symbols, get_callers, get_file_summary, edit_file) over Read/Grep/Glob/Edit."
const codexSessionStartCommand = "printf '%s\\n' '" + codexSessionStartMessage + "'"
const codexSessionStartWindowsCommand = "powershell -NoProfile -Command \"Write-Output '" + codexSessionStartMessage + "'\""

type Adapter struct{}

func New() *Adapter                { return &Adapter{} }
func (a *Adapter) Name() string    { return Name }
func (a *Adapter) DocsURL() string { return DocsURL }

// Detect checks for the codex CLI on PATH or ~/.codex/.
func (a *Adapter) Detect(env agents.Env) (bool, error) {
	if p, err := exec.LookPath("codex"); err == nil && p != "" {
		return true, nil
	}
	if env.Home == "" {
		return false, nil
	}
	if _, err := os.Stat(filepath.Join(env.Home, ".codex")); err == nil {
		return true, nil
	}
	return false, nil
}

func (a *Adapter) Plan(env agents.Env) (*agents.Plan, error) {
	p := &agents.Plan{}
	if env.Home != "" {
		keys := []string{"mcp_servers"}
		if env.InstallHooks {
			keys = append(keys, "hooks")
		}
		p.Files = append(p.Files, agents.FileAction{
			Path:   filepath.Join(env.Home, ".codex", "config.toml"),
			Action: agents.ActionWouldMerge,
			Keys:   keys,
		})
	}
	if env.Mode != agents.ModeGlobal && env.SkillsRouting != "" {
		p.Files = append(p.Files, agents.FileAction{
			Path: filepath.Join(env.Root, "AGENTS.md"), Action: agents.ActionWouldMerge,
			Keys: []string{"communities-block"},
		})
	}
	return p, nil
}

func (a *Adapter) Apply(env agents.Env, opts agents.ApplyOpts) (*agents.Result, error) {
	res := &agents.Result{Name: Name, DocsURL: DocsURL}
	detected, _ := a.Detect(env)
	res.Detected = detected
	if !detected && !opts.ForceDetect {
		internalutil.Logf(env.Stderr, "[gortex init] skip Codex setup (codex not detected)")
		return res, nil
	}
	if env.Home == "" {
		return res, fmt.Errorf("codex: requires a resolved home directory")
	}
	internalutil.Logf(env.Stderr, "[gortex init] setting up OpenAI Codex CLI integration...")

	path := filepath.Join(env.Home, ".codex", "config.toml")
	action, err := agents.MergeTOML(env.Stderr, path, func(root map[string]any, _ bool) (bool, error) {
		changed := false
		servers, ok := root["mcp_servers"].(map[string]any)
		if !ok {
			servers = make(map[string]any)
		}
		if _, exists := servers["gortex"]; !exists || opts.Force {
			servers["gortex"] = map[string]any{
				"command": "gortex",
				"args":    []string{"mcp"},
				"env": map[string]any{
					"GORTEX_INDEX_WORKERS": "8",
				},
			}
			root["mcp_servers"] = servers
			changed = true
		}

		if env.InstallHooks && upsertSessionStartHook(root, opts) {
			changed = true
		}
		return changed, nil
	}, opts)
	if err != nil {
		return res, err
	}
	res.Files = append(res.Files, action)

	// Repo-local community routing → AGENTS.md (also read by
	// OpenCode; both adapters upsert the same marker-guarded block,
	// so repeat runs converge). Skipped in global mode (AGENTS.md
	// is per-repo) and when no communities were generated.
	if env.Mode != agents.ModeGlobal && env.SkillsRouting != "" {
		agentsMdPath := filepath.Join(env.Root, "AGENTS.md")
		routingAction, err := agents.UpsertMarkedBlock(env.Stderr, agentsMdPath, env.SkillsRouting,
			agents.CommunitiesStartMarker, agents.CommunitiesEndMarker, opts)
		if err != nil {
			return res, err
		}
		res.Files = append(res.Files, routingAction)
	}

	res.Configured = true
	return res, nil
}

func upsertSessionStartHook(root map[string]any, opts agents.ApplyOpts) bool {
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		if _, exists := root["hooks"]; exists {
			return false
		}
		hooks = make(map[string]any)
	}

	entries, ok := codexHookList(hooks["SessionStart"])
	if !ok {
		return false
	}

	found := false
	kept := make([]any, 0, len(entries)+1)
	for _, entry := range entries {
		if codexHookEntryIsGortexSessionStart(entry) {
			found = true
			if opts.Force {
				continue
			}
		}
		kept = append(kept, entry)
	}
	if found && !opts.Force {
		return false
	}

	hooks["SessionStart"] = append(kept, codexSessionStartHookEntry())
	root["hooks"] = hooks
	return true
}

func codexHookList(v any) ([]any, bool) {
	if v == nil {
		return nil, true
	}
	switch list := v.(type) {
	case []any:
		return append([]any(nil), list...), true
	case []map[string]any:
		out := make([]any, 0, len(list))
		for _, entry := range list {
			out = append(out, entry)
		}
		return out, true
	default:
		return nil, false
	}
}

func codexHookEntryIsGortexSessionStart(entry any) bool {
	group, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	handlers, ok := codexHookList(group["hooks"])
	if !ok {
		return false
	}
	for _, handler := range handlers {
		hm, ok := handler.(map[string]any)
		if !ok {
			continue
		}
		if cmd, _ := hm["command"].(string); cmd == codexSessionStartCommand {
			return true
		}
		if cmd, _ := hm["command_windows"].(string); cmd == codexSessionStartWindowsCommand {
			return true
		}
	}
	return false
}

func codexSessionStartHookEntry() map[string]any {
	return map[string]any{
		"matcher": codexSessionStartMatcher,
		"hooks": []any{
			map[string]any{
				"type":            "command",
				"command":         codexSessionStartCommand,
				"command_windows": codexSessionStartWindowsCommand,
				"timeout":         5,
				"statusMessage":   "Loading Gortex graph orientation...",
			},
		},
	}
}
