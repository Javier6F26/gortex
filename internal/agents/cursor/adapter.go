// Package cursor implements the Gortex init integration for
// Cursor. Writes .cursor/mcp.json (project-level) and, when --global
// is effective, ~/.cursor/mcp.json (user-level).
//
// Schema: standard {"mcpServers": {<name>: {command, args, env}}}.
package cursor

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/zzet/gortex/internal/agents"
	"github.com/zzet/gortex/internal/agents/internalutil"
)

const Name = "cursor"
const DocsURL = "https://docs.cursor.com/en/context/mcp"

type Adapter struct{}

func New() *Adapter                { return &Adapter{} }
func (a *Adapter) Name() string    { return Name }
func (a *Adapter) DocsURL() string { return DocsURL }

// Detect succeeds when any of: project has .cursor/, user has
// ~/.cursor/, or "cursor" is on PATH.
func (a *Adapter) Detect(env agents.Env) (bool, error) {
	if _, err := os.Stat(filepath.Join(env.Root, ".cursor")); err == nil {
		return true, nil
	}
	if env.Home != "" {
		if _, err := os.Stat(filepath.Join(env.Home, ".cursor")); err == nil {
			return true, nil
		}
	}
	if p, err := exec.LookPath("cursor"); err == nil && p != "" {
		return true, nil
	}
	return false, nil
}

func (a *Adapter) Plan(env agents.Env) (*agents.Plan, error) {
	return &agents.Plan{Files: []agents.FileAction{
		{Path: mcpConfigPath(env), Action: agents.ActionWouldMerge, Keys: []string{"mcpServers"}},
		{Path: rulesPath(env), Action: agents.ActionWouldCreate, Keys: []string{"gortex-rule"}},
	}}, nil
}

// mcpConfigPath returns the mcp.json path for the given mode.
// Project mode: .cursor/mcp.json; global mode: ~/.cursor/mcp.json.
// Cursor reads both and prefers project when a key is defined in
// both.
func mcpConfigPath(env agents.Env) string {
	if env.Mode == agents.ModeGlobal && env.Home != "" {
		return filepath.Join(env.Home, ".cursor", "mcp.json")
	}
	return filepath.Join(env.Root, ".cursor", "mcp.json")
}

// rulesPath returns the project-scoped MDC file Cursor auto-applies on
// every chat turn. Cursor does not support user-level MDC rules (they
// live in the app's Settings UI), so we always write project-scoped.
func rulesPath(env agents.Env) string {
	return filepath.Join(env.Root, ".cursor", "rules", "gortex.mdc")
}

func (a *Adapter) Apply(env agents.Env, opts agents.ApplyOpts) (*agents.Result, error) {
	res := &agents.Result{Name: Name, DocsURL: DocsURL}
	detected, _ := a.Detect(env)
	res.Detected = detected
	if !detected {
		internalutil.Logf(env.Stderr, "[gortex init] skip Cursor setup (Cursor not detected)")
		return res, nil
	}
	internalutil.Logf(env.Stderr, "[gortex init] setting up Cursor IDE integration...")

	path := mcpConfigPath(env)
	action, err := agents.MergeJSON(env.Stderr, path, func(root map[string]any, _ bool) (bool, error) {
		return agents.UpsertMCPServer(root, "gortex", agents.DefaultGortexMCPEntry(), opts), nil
	}, opts)
	if err != nil {
		return res, err
	}
	res.Files = append(res.Files, action)

	// MDC rules file — one-rule-per-file format, create only when
	// missing so user edits to .cursor/rules/gortex.mdc survive init
	// re-runs. Cursor applies `alwaysApply: true` rules on every chat
	// turn, which is exactly what we want for the MANDATORY block.
	ruleAction, err := agents.WriteIfNotExists(env.Stderr, rulesPath(env), agents.CursorMDCFrontmatter(agents.InstructionsBody), opts)
	if err != nil {
		return res, err
	}
	res.Files = append(res.Files, ruleAction)

	res.Configured = true
	return res, nil
}
