package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zzet/gortex/internal/agents"
	"github.com/zzet/gortex/internal/agents/aider"
	"github.com/zzet/gortex/internal/agents/antigravity"
	"github.com/zzet/gortex/internal/agents/claudecode"
	"github.com/zzet/gortex/internal/agents/cline"
	"github.com/zzet/gortex/internal/agents/codex"
	"github.com/zzet/gortex/internal/agents/continuedev"
	"github.com/zzet/gortex/internal/agents/cursor"
	"github.com/zzet/gortex/internal/agents/gemini"
	"github.com/zzet/gortex/internal/agents/kilocode"
	"github.com/zzet/gortex/internal/agents/kiro"
	"github.com/zzet/gortex/internal/agents/opencode"
	"github.com/zzet/gortex/internal/agents/openclaw"
	"github.com/zzet/gortex/internal/agents/vscode"
	"github.com/zzet/gortex/internal/agents/windsurf"
	"github.com/zzet/gortex/internal/agents/zed"
	"github.com/zzet/gortex/internal/claudemd"
	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/indexer"
	"github.com/zzet/gortex/internal/parser"
	"github.com/zzet/gortex/internal/parser/languages"
	"github.com/zzet/gortex/internal/query"
	genskills "github.com/zzet/gortex/internal/skills"
)

// Per-flag globals. Behaviour flags stay separate from UX flags so
// the wizard and the orchestrator read them without cross-pollution.
var (
	// Core behaviour
	initAnalyze      bool
	initInstallHooks = true
	initNoHooks      bool
	initHooksOnly    bool

	// Community skills generation (replaces the old `gortex skills`).
	initSkills          = true
	initNoSkills        bool
	initSkillsMinSize   int
	initSkillsMaxSkills int

	// Non-interactive / reporting knobs
	initYes        bool
	initAgents     string
	initAgentsSkip string
	initJSON       bool
	initDryRun     bool
	initForce      bool
)

var initCmd = &cobra.Command{
	Use:   "init [path]",
	Short: "Wire Gortex into the current repository for every detected AI coding assistant",
	Long: `Configure Gortex for this repository: per-repo MCP config, (optional)
hooks, community-derived routing, and a codebase overview.

For one-time machine-wide setup (user MCP config, user skills /
Knowledge Items, user hooks), run ` + "`gortex install`" + ` once.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runInit,
}

func init() {
	initCmd.Flags().BoolVar(&initAnalyze, "analyze", false, "index the repo to generate a richer CLAUDE.md with codebase overview")
	initCmd.Flags().BoolVar(&initInstallHooks, "hooks", true, "install Claude Code hooks (PreToolUse + PreCompact + Stop); use --no-hooks to skip")
	initCmd.Flags().BoolVar(&initNoHooks, "no-hooks", false, "skip installing Claude Code hooks (inverse of --hooks)")
	initCmd.Flags().BoolVar(&initHooksOnly, "hooks-only", false, "only install/update Claude Code hooks in .claude/settings.local.json, skip everything else")

	initCmd.Flags().BoolVar(&initSkills, "skills", true, "generate per-community routing + SKILL.md files; use --no-skills to skip")
	initCmd.Flags().BoolVar(&initNoSkills, "no-skills", false, "skip community-skill generation (inverse of --skills)")
	initCmd.Flags().IntVar(&initSkillsMinSize, "skills-min-size", 3, "minimum community size to generate a skill")
	initCmd.Flags().IntVar(&initSkillsMaxSkills, "skills-max", 20, "maximum number of skills to generate")

	initCmd.Flags().BoolVarP(&initYes, "yes", "y", false, "skip the interactive wizard (implied when stdin is not a TTY)")
	initCmd.Flags().StringVar(&initAgents, "agents", "", "comma-separated list of agents to configure ('auto' means every registered adapter)")
	initCmd.Flags().StringVar(&initAgentsSkip, "agents-skip", "", "comma-separated list of agents to skip (composable with --agents)")
	initCmd.Flags().BoolVar(&initJSON, "json", false, "emit a structured JSON report on stdout")
	initCmd.Flags().BoolVar(&initDryRun, "dry-run", false, "plan writes without modifying disk")
	initCmd.Flags().BoolVar(&initForce, "force", false, "overwrite keys we would otherwise preserve during a merge")

	rootCmd.AddCommand(initCmd)
}

// buildRegistry wires up every registered adapter. Registration
// order is also execution order — Claude Code first because it's
// the primary integration, then alphabetical for stable --json.
func buildRegistry() *agents.Registry {
	r := agents.NewRegistry()
	r.Register(claudecode.New())
	r.Register(aider.New())
	r.Register(antigravity.New())
	r.Register(cline.New())
	r.Register(codex.New())
	r.Register(continuedev.New())
	r.Register(cursor.New())
	r.Register(gemini.New())
	r.Register(kilocode.New())
	r.Register(kiro.New())
	r.Register(opencode.New())
	r.Register(openclaw.New())
	r.Register(vscode.New())
	r.Register(windsurf.New())
	r.Register(zed.New())
	return r
}

func runInit(cmd *cobra.Command, args []string) error {
	root := "."
	if len(args) > 0 {
		root = args[0]
	}

	if initNoHooks {
		initInstallHooks = false
	}
	if initNoSkills {
		initSkills = false
	}

	// Interactive wizard — only asks about hooks now (global/per-repo
	// split is owned by the separate `gortex install` command).
	if !initYes && !initHooksOnly && isInteractive() {
		hooksPreset := cmd.Flags().Changed("hooks") || cmd.Flags().Changed("no-hooks")
		if choice, ran := runInteractiveInit(os.Stdin, cmd.ErrOrStderr(), hooksPreset); ran {
			if !hooksPreset {
				initInstallHooks = choice.Hooks
			}
		}
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}

	// --hooks-only short-circuit: install/heal Claude Code hooks
	// and exit. Everything else is a no-op.
	if initHooksOnly {
		settingsPath := filepath.Join(absRoot, ".claude", "settings.local.json")
		if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
			return err
		}
		action, err := claudecode.InstallHook(cmd.ErrOrStderr(), settingsPath, agents.ApplyOpts{DryRun: initDryRun, Force: initForce})
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "[gortex init --hooks-only] %s %s\n", action.Action, action.Path)
		return nil
	}

	home, _ := os.UserHomeDir()
	env := agents.Env{
		Root:         absRoot,
		Home:         home,
		HookCommand:  claudecode.ResolveHookCommand(cmd.ErrOrStderr()),
		Mode:         agents.ModeProject,
		InstallHooks: initInstallHooks,
		AnalyzeRepo:  initAnalyze,
		Stderr:       cmd.ErrOrStderr(),
	}

	// Indexing powers both --analyze (codebase overview in
	// CLAUDE.md) and --skills (community routing in every per-repo
	// instructions surface). Index once, feed both.
	needIndex := initAnalyze || initSkills
	if needIndex {
		g, idxErr := indexRepoForInit(absRoot, cmd.ErrOrStderr())
		if idxErr != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "[gortex init] indexing failed: %v — proceeding without analysis/skills\n", idxErr)
		} else {
			if initAnalyze {
				eng := query.NewEngine(g)
				env.AnalyzedOverview = claudemd.Generate(eng, 180)
			}
			if initSkills {
				generated, routing := genskills.Build(g, genskills.BuildOpts{
					MinSize:   initSkillsMinSize,
					MaxSkills: initSkillsMaxSkills,
				})
				if len(generated) > 0 {
					env.GeneratedSkills = toEnvSkills(generated)
					env.SkillsRouting = routing
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "[gortex init] generated %d community skill(s)\n", len(generated))
				} else {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "[gortex init] no communities large enough (min-size: %d) — skipping skills\n", initSkillsMinSize)
				}
			}
		}
	}

	registry := buildRegistry()
	selected, err := registry.Filter(initAgents, initAgentsSkip)
	if err != nil {
		return err
	}

	opts := agents.ApplyOpts{DryRun: initDryRun, Force: initForce}
	results := make([]*agents.Result, 0, len(selected))
	for _, a := range selected {
		r, err := a.Apply(env, opts)
		if err != nil {
			if a.Name() == claudecode.Name {
				return fmt.Errorf("%s: %w", a.Name(), err)
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "[gortex init] warning: %s setup failed: %v\n", a.Name(), err)
		}
		if r != nil {
			results = append(results, r)
		}
	}

	// Always update Gortex's own global config so the daemon picks
	// up this repo next time it starts (harmless when no daemon).
	if !initDryRun {
		if err := ensureGlobalConfig(absRoot); err != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "[gortex init] warning: could not update global config: %v\n", err)
		}
	}

	if initJSON {
		if err := emitJSONReport(cmd.OutOrStdout(), results, opts); err != nil {
			return err
		}
	}
	emitHumanSummary(cmd.ErrOrStderr(), results, opts)
	return nil
}

// toEnvSkills converts the skills generator's output into the
// agents.GeneratedSkill payload carried on Env. The two shapes are
// identical today; the mirror keeps the agents package free of the
// internal/skills dependency.
func toEnvSkills(src []genskills.GeneratedSkill) []agents.GeneratedSkill {
	out := make([]agents.GeneratedSkill, len(src))
	for i, s := range src {
		out[i] = agents.GeneratedSkill{
			CommunityID: s.CommunityID,
			Label:       s.Label,
			DirName:     s.DirName,
			Content:     s.Content,
		}
	}
	return out
}

// indexRepoForInit runs a one-shot index of the repo. Kept inside
// cmd/gortex (not an adapter) because the indexer pulls in many
// gortex-internal packages we'd rather not leak into internal/agents.
func indexRepoForInit(root string, w io.Writer) (*graph.Graph, error) {
	logger := newLogger()
	defer func() { _ = logger.Sync() }()

	cfg, err := config.Load("")
	if err != nil {
		cfg = &config.Config{}
	}

	g := graph.New()
	reg := parser.NewRegistry()
	languages.RegisterAll(reg)

	idx := indexer.New(g, reg, cfg.Index, logger)
	_, _ = fmt.Fprintf(w, "[gortex init] indexing %s...\n", root)
	result, err := idx.Index(root)
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(w, "[gortex init] indexed %d files (%d nodes, %d edges) in %dms\n",
		result.FileCount, result.NodeCount, result.EdgeCount, result.DurationMs)
	return g, nil
}

// emitJSONReport writes a single JSON object to w. Shape kept
// compatible with earlier releases (agents array, dry_run, force)
// plus a mode discriminator so the install/init outputs are
// distinguishable.
func emitJSONReport(w io.Writer, results []*agents.Result, opts agents.ApplyOpts) error {
	payload := map[string]any{
		"dry_run": opts.DryRun,
		"force":   opts.Force,
		"mode":    "project",
		"agents":  results,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

// emitHumanSummary prints the per-agent file counts to stderr.
func emitHumanSummary(w io.Writer, results []*agents.Result, opts agents.ApplyOpts) {
	_, _ = fmt.Fprintf(w, "\n[gortex init] done")
	if opts.DryRun {
		_, _ = fmt.Fprintf(w, " (dry-run — no files written)")
	}
	_, _ = fmt.Fprintf(w, ":\n")
	for _, r := range results {
		if r == nil {
			continue
		}
		detected := "detected"
		if !r.Detected {
			detected = "not detected"
		}
		_, _ = fmt.Fprintf(w, "  • %s — %s, %d file(s) [%s]\n", r.Name, detected, len(r.Files), countByAction(r.Files))
	}
	_, _ = fmt.Fprintln(w, "\nCommit .mcp.json, .claude/commands/, .claude/settings.json, CLAUDE.md, and any detected agent configs so your team gets Gortex automatically.")
	_, _ = fmt.Fprintln(w, "If you haven't already, run `gortex install` once per machine to set up user-level integration.")
}

// countByAction renders "create=3 merge=1 skip=2" style line.
func countByAction(files []agents.FileAction) string {
	var c, m, s, wc, wm int
	for _, f := range files {
		switch f.Action {
		case agents.ActionCreate:
			c++
		case agents.ActionMerge:
			m++
		case agents.ActionSkip:
			s++
		case agents.ActionWouldCreate:
			wc++
		case agents.ActionWouldMerge:
			wm++
		}
	}
	parts := []string{}
	if c > 0 {
		parts = append(parts, fmt.Sprintf("create=%d", c))
	}
	if m > 0 {
		parts = append(parts, fmt.Sprintf("merge=%d", m))
	}
	if s > 0 {
		parts = append(parts, fmt.Sprintf("skip=%d", s))
	}
	if wc > 0 {
		parts = append(parts, fmt.Sprintf("would-create=%d", wc))
	}
	if wm > 0 {
		parts = append(parts, fmt.Sprintf("would-merge=%d", wm))
	}
	return strings.Join(parts, " ")
}

// ensureGlobalConfig adds this repo to ~/.config/gortex/config.yaml
// so the daemon picks it up on its next restart. Skipped in --dry-run.
func ensureGlobalConfig(root string) error {
	gc, err := config.LoadGlobal()
	if err != nil {
		return err
	}
	if err := gc.AddRepo(config.RepoEntry{Path: root}); err != nil {
		return err
	}
	if err := gc.Save(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stderr, "[gortex init] updated global config at %s\n", gc.ConfigPath())
	return nil
}
