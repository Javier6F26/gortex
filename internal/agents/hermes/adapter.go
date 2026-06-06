// Package hermes implements the Gortex init/install integration for
// NousResearch Hermes (https://github.com/NousResearch/hermes-agent),
// a CLI agent-orchestrator that consumes MCP servers.
//
// Hermes is a user-level agent, not a repo-scoped IDE: it stores all
// state under ~/.hermes/ and the gortex daemon already resolves the
// active workspace per MCP session, so one global server entry serves
// every repo. We therefore write user-level artifacts in both
// `gortex init` (ModeProject) and `gortex install` (ModeGlobal), the
// same as the openclaw / antigravity adapters — the writes are
// idempotent, so running both is harmless.
//
// Three surfaces are configured:
//
//  1. Global ~/.hermes/config.yaml — upsert a `gortex` stdio server
//     under the snake_case `mcp_servers` map, comment-preservingly
//     (the config is hand-edited and comment-rich).
//  2. Every existing ~/.hermes/profiles/<name>/config.yaml — Hermes
//     profiles can re-declare their own `mcp_servers` block rather
//     than inheriting the global one, so we upsert the gortex stanza
//     into each profile config that already exists. This guarantees
//     every profile resolves the gortex tools regardless of the
//     global↔profile merge semantics. (We never create new profiles.)
//  3. A user-level skill at ~/.hermes/skills/gortex/SKILL.md teaching
//     the agent to prefer gortex graph tools — Hermes' equivalent of
//     the Claude Code / Antigravity user-level instruction surface.
package hermes

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zzet/gortex/internal/agents"
	"github.com/zzet/gortex/internal/agents/internalutil"
	yaml "gopkg.in/yaml.v3"
)

const Name = "hermes"
const DocsURL = "https://hermes-agent.nousresearch.com/docs/user-guide/features/mcp"

type Adapter struct{}

func New() *Adapter                { return &Adapter{} }
func (a *Adapter) Name() string    { return Name }
func (a *Adapter) DocsURL() string { return DocsURL }

// Detect returns true when Hermes is installed or its home directory
// exists. False means "skip", not an error — a machine without Hermes
// gets no ~/.hermes writes.
func (a *Adapter) Detect(env agents.Env) (bool, error) {
	if p, err := exec.LookPath("hermes"); err == nil && p != "" {
		return true, nil
	}
	if env.Home == "" {
		return false, nil
	}
	if _, err := os.Stat(hermesDir(env.Home)); err == nil {
		return true, nil
	}
	return false, nil
}

func (a *Adapter) Plan(env agents.Env) (*agents.Plan, error) {
	if env.Home == "" {
		return &agents.Plan{}, nil
	}
	files := []agents.FileAction{
		{Path: globalConfigPath(env.Home), Action: agents.ActionWouldMerge, Keys: []string{"mcp_servers"}},
	}
	for _, p := range profileConfigPaths(env.Home) {
		files = append(files, agents.FileAction{Path: p, Action: agents.ActionWouldMerge, Keys: []string{"mcp_servers"}})
	}
	files = append(files, agents.FileAction{Path: skillPath(env.Home, SkillName), Action: agents.ActionWouldCreate})
	for _, name := range RoutingSkillNames() {
		files = append(files, agents.FileAction{Path: skillPath(env.Home, name), Action: agents.ActionWouldCreate})
	}
	return &agents.Plan{Files: files}, nil
}

func (a *Adapter) Apply(env agents.Env, opts agents.ApplyOpts) (*agents.Result, error) {
	res := &agents.Result{Name: Name, DocsURL: DocsURL}
	detected, _ := a.Detect(env)
	res.Detected = detected
	if !detected && !opts.ForceDetect {
		internalutil.Logf(env.Stderr, "[gortex init] skip Hermes setup (hermes not detected)")
		return res, nil
	}
	if env.Home == "" {
		return res, fmt.Errorf("hermes: requires a resolved home directory")
	}
	internalutil.Logf(env.Stderr, "[gortex init] setting up Hermes integration...")

	command := resolveGortexCommand()

	// 1. Global config — the entry every profile inherits when it
	//    doesn't re-declare its own server map.
	globalAction, err := upsertGortexServer(env.Stderr, globalConfigPath(env.Home), command, opts)
	if err != nil {
		return res, fmt.Errorf("hermes global config: %w", err)
	}
	res.Files = append(res.Files, globalAction)

	// 2. Per-profile configs — Hermes profiles may carry their own
	//    mcp_servers block, so upsert into each existing one too. A
	//    failure on one profile is a warning, not fatal: the global
	//    entry still covers profiles that do inherit.
	for _, profilePath := range profileConfigPaths(env.Home) {
		profileAction, perr := upsertGortexServer(env.Stderr, profilePath, command, opts)
		if perr != nil {
			// Non-fatal: the global stanza still covers profiles that
			// inherit. But this profile does NOT inherit, so record the
			// failure on the result — not just stderr — otherwise a
			// Configured=true silently masks a profile left unconfigured.
			internalutil.Warnf(env.Stderr, "hermes profile %s: %v", profilePath, perr)
			res.Warnings = append(res.Warnings, fmt.Sprintf("profile %s not configured: %v", profilePath, perr))
			continue
		}
		res.Files = append(res.Files, profileAction)
	}

	// 3. User-level skills — the master `gortex` guide plus the
	//    per-task routing playbooks (explore / impact / refactor / …),
	//    mirroring the Claude Code user-level skill set. Each is skipped
	//    when it already exists so user edits survive a re-install.
	masterAction, err := agents.WriteIfNotExists(env.Stderr, skillPath(env.Home, SkillName), SkillBody(), opts)
	if err != nil {
		return res, fmt.Errorf("hermes skill: %w", err)
	}
	res.Files = append(res.Files, masterAction)

	routing := RoutingSkills()
	for _, name := range RoutingSkillNames() {
		action, rerr := agents.WriteIfNotExists(env.Stderr, skillPath(env.Home, name), routing[name], opts)
		if rerr != nil {
			internalutil.Warnf(env.Stderr, "hermes skill %s: %v", name, rerr)
			continue
		}
		res.Files = append(res.Files, action)
	}

	res.Configured = true
	return res, nil
}

// upsertGortexServer merges the gortex stdio stanza into the
// `mcp_servers` map of a Hermes YAML config, preserving comments and
// unrelated keys.
func upsertGortexServer(w io.Writer, path, command string, opts agents.ApplyOpts) (agents.FileAction, error) {
	return agents.MergeYAML(w, path, func(root *yaml.Node, _ bool) (bool, error) {
		return agents.UpsertYAMLMapEntry(root, "mcp_servers", gortexServerName, gortexMCPEntry(command), opts.Force)
	}, opts)
}

// resolveGortexCommand returns the command Hermes should launch for the
// gortex MCP server. It prefers a stable absolute path so the entry
// works regardless of how Hermes' subprocess PATH is set up:
//
//  1. os.Executable() — but only when it actually points at an installed
//     `gortex` binary. Under `go run`, os.Executable() is a temp build
//     that is deleted on exit (and may even be *named* gortex), so we
//     additionally reject any path under the temp dir.
//  2. exec.LookPath("gortex") — a stable PATH install (homebrew / go
//     install).
//  3. the bare "gortex" name as a last resort.
func resolveGortexCommand() string {
	if exe, err := os.Executable(); err == nil && exe != "" {
		base := filepath.Base(exe)
		base = strings.TrimSuffix(base, filepath.Ext(base)) // drop .exe on Windows
		underTemp := strings.HasPrefix(exe, filepath.Clean(os.TempDir())+string(os.PathSeparator))
		if base == "gortex" && !underTemp {
			return exe
		}
	}
	if p, err := exec.LookPath("gortex"); err == nil && p != "" {
		return p
	}
	return "gortex"
}

// hermesDir is the ~/.hermes root.
func hermesDir(home string) string { return filepath.Join(home, ".hermes") }

// globalConfigPath is ~/.hermes/config.yaml.
func globalConfigPath(home string) string { return filepath.Join(hermesDir(home), "config.yaml") }

// skillPath is ~/.hermes/skills/<category>/<name>/SKILL.md. Hermes
// discovers SKILL.md files recursively, and its convention is to group
// skills under a category folder rather than at the skills root.
func skillPath(home, name string) string {
	return filepath.Join(hermesDir(home), "skills", skillCategory(name), name, "SKILL.md")
}

// skillCategory returns the ~/.hermes/skills subdirectory a gortex skill
// lives under. We reuse the routing-skill taxonomy so each playbook
// lands in its topical folder (navigation / analysis / debugging / …)
// and the master guide under code-intelligence — keeping the skills root
// uncluttered and matching how Hermes' own skills are organised.
func skillCategory(name string) string {
	if name == SkillName {
		return masterSkillCategory
	}
	_, category := routingSkillTaxonomy(name)
	return category
}

// profileConfigPaths returns the config.yaml of every existing Hermes
// profile under ~/.hermes/profiles/<name>/, sorted for a stable
// install report and deterministic tests. Returns nil when the
// profiles directory is absent.
func profileConfigPaths(home string) []string {
	matches, err := filepath.Glob(filepath.Join(hermesDir(home), "profiles", "*", "config.yaml"))
	if err != nil || len(matches) == 0 {
		return nil
	}
	sort.Strings(matches)
	return matches
}
