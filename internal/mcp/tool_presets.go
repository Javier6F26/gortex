package mcp

import (
	"os"
	"strings"

	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/daemon"
)

// ToolPolicyConfig is the operator-facing description of a restricted
// tool surface: a named preset, per-tool allow/deny deltas, and a mode.
// It is the wire between the `mcp.tools` config block, the GORTEX_TOOLS
// / GORTEX_TOOLS_MODE env overrides, and the resolved in-memory
// toolPolicy. Zero value (empty preset, no deltas) means "no
// restriction" — the full surface.
type ToolPolicyConfig struct {
	Preset string
	Mode   string // "hide" | "defer" — default hide
	Allow  []string
	Deny   []string
}

const (
	// toolPolicyModeHide removes non-allowed tools from tools/list and
	// hard-blocks calls to them. The minimal, locked-down surface a
	// headless harness wants — works identically on every client.
	toolPolicyModeHide = "hide"
	// toolPolicyModeDefer keeps non-allowed tools out of the cold
	// tools/list but still reachable via the tools_search discovery
	// tool (which promotes on demand). Only effective on clients that
	// honour notifications/tools/list_changed.
	toolPolicyModeDefer = "defer"

	toolPresetEnv     = "GORTEX_TOOLS"
	toolPresetModeEnv = "GORTEX_TOOLS_MODE"
)

// editPresetTools is the minimal headless code-editing surface: orient,
// navigate, mutate, verify. Sized so an agent can edit code safely on a
// remote box without the full 170-tool catalogue. tool_profile and
// tools_search are always kept on top of any preset (isAlwaysKeptTool).
var editPresetTools = []string{
	// orient + read
	"smart_context", "get_editing_context", "read_file", "get_symbol_source",
	"get_file_summary", "get_symbol",
	// navigate
	"search_symbols", "search_text", "find_files", "find_usages", "get_callers",
	// mutate
	"edit_file", "edit_symbol", "write_file", "batch_edit", "rename_symbol",
	// verify
	"verify_change", "get_test_targets", "check_guards", "get_diagnostics",
	// orientation
	"graph_stats",
}

// navPresetTools is the read-only navigation / exploration surface — no
// editing tools at all.
var navPresetTools = []string{
	"smart_context", "get_editing_context", "read_file", "get_symbol_source",
	"get_file_summary", "get_symbol",
	"search_symbols", "search_text", "find_files", "find_usages",
	"find_implementations", "find_overrides", "get_callers", "get_call_chain",
	"get_dependencies", "get_dependents", "get_repo_outline", "graph_stats",
}

// builtinToolPresetSet resolves a preset name to its explicit allow-set.
// A nil set with denyMutating=false is the sentinel for "no explicit
// restriction" (the full surface); `readonly` carries denyMutating=true
// instead of an explicit list so it tracks the authoritative
// daemon.MutatingTools set as it evolves. known=false flags an
// unrecognised preset name.
func builtinToolPresetSet(name string) (set map[string]bool, denyMutating, known bool) {
	switch name {
	case "", "full", "all":
		return nil, false, true
	case "readonly", "read-only", "read_only":
		return nil, true, true
	case "edit", "editor", "edit-harness":
		return toToolSet(editPresetTools), false, true
	case "nav", "navigate", "explore":
		return toToolSet(navPresetTools), false, true
	default:
		return nil, false, false
	}
}

// builtinPresetNames lists the recognised preset names for diagnostics.
var builtinPresetNames = []string{"full", "readonly", "edit", "nav"}

// toolPolicy is the resolved, in-memory restriction applied to the tool
// surface by the lazy registry (defer mode) and toolSurfaceFilter /
// checkToolGate (hide mode). The zero/nil policy allows everything.
type toolPolicy struct {
	preset       string
	mode         string
	explicit     map[string]bool // non-nil => base surface is exactly this set
	denyMutating bool            // drop daemon.MutatingTools (the `readonly` preset)
	allow        map[string]bool // force-include (overrides the preset)
	deny         map[string]bool // force-exclude (overrides everything)
	active       bool
}

func toToolSet(names []string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// normalizeToolMode maps a mode string to hide|defer (default hide).
func normalizeToolMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case toolPolicyModeDefer, "lazy", "search":
		return toolPolicyModeDefer
	default:
		return toolPolicyModeHide
	}
}

// newToolPolicy resolves a ToolPolicyConfig into a toolPolicy. An
// unrecognised preset name is logged and downgraded to the full surface
// (fail-open — a typo never silently strands an agent with no tools).
func newToolPolicy(cfg ToolPolicyConfig, logger *zap.Logger) *toolPolicy {
	preset := strings.ToLower(strings.TrimSpace(cfg.Preset))
	set, denyMutating, known := builtinToolPresetSet(preset)
	if !known {
		if logger != nil {
			logger.Warn("unknown MCP tool preset; serving the full surface",
				zap.String("preset", cfg.Preset),
				zap.Strings("known", builtinPresetNames))
		}
		preset, set, denyMutating = "full", nil, false
	}
	if preset == "" || preset == "all" {
		preset = "full"
	}
	allow := toToolSet(cfg.Allow)
	deny := toToolSet(cfg.Deny)
	active := set != nil || denyMutating || len(allow) > 0 || len(deny) > 0
	return &toolPolicy{
		preset:       preset,
		mode:         normalizeToolMode(cfg.Mode),
		explicit:     set,
		denyMutating: denyMutating,
		allow:        allow,
		deny:         deny,
		active:       active,
	}
}

// isAlwaysKeptTool: introspection (tool_profile) and discovery
// (tools_search) stay reachable under every preset so an agent can
// always see its surface and, in defer mode, discover more. An explicit
// deny still wins (checked before this in allows).
func isAlwaysKeptTool(name string) bool {
	return name == "tool_profile" || name == LazyToolsSearchName
}

// allows reports whether name is part of this policy's allowed surface.
// A nil or inactive policy allows everything.
func (p *toolPolicy) allows(name string) bool {
	if !p.isActive() {
		return true
	}
	if p.deny[name] {
		return false
	}
	if isAlwaysKeptTool(name) {
		return true
	}
	if p.allow[name] {
		return true
	}
	if p.explicit != nil {
		return p.explicit[name]
	}
	if p.denyMutating && daemon.IsMutating(name) {
		return false
	}
	return true
}

func (p *toolPolicy) isActive() bool { return p != nil && p.active }
func (p *toolPolicy) hideMode() bool  { return p.isActive() && p.mode == toolPolicyModeHide }
func (p *toolPolicy) deferMode() bool { return p.isActive() && p.mode == toolPolicyModeDefer }

// toolPolicyConfigFromEnv reads GORTEX_TOOLS / GORTEX_TOOLS_MODE. The
// bool reports whether either var was set.
func toolPolicyConfigFromEnv() (ToolPolicyConfig, bool) {
	spec := strings.TrimSpace(os.Getenv(toolPresetEnv))
	mode := strings.TrimSpace(os.Getenv(toolPresetModeEnv))
	if spec == "" && mode == "" {
		return ToolPolicyConfig{}, false
	}
	cfg := parseToolSpec(spec)
	if mode != "" {
		cfg.Mode = mode
	}
	return cfg, true
}

// parseToolSpec parses a spec like "edit,+find_files,-write_file": the
// first bare token is the preset; +name adds an allow delta, -name a
// deny delta.
func parseToolSpec(spec string) ToolPolicyConfig {
	var cfg ToolPolicyConfig
	for _, tok := range strings.Split(spec, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		switch {
		case strings.HasPrefix(tok, "+"):
			cfg.Allow = append(cfg.Allow, strings.TrimPrefix(tok, "+"))
		case strings.HasPrefix(tok, "-"):
			cfg.Deny = append(cfg.Deny, strings.TrimPrefix(tok, "-"))
		default:
			if cfg.Preset == "" {
				cfg.Preset = tok
			}
		}
	}
	return cfg
}

// ParseToolSpec parses a "preset,+tool,-tool" spec into its parts. The
// first bare token is the preset; +name / -name are allow / deny deltas.
// Exported for CLI flag folding (cmd/gortex).
func ParseToolSpec(spec string) (preset string, allow, deny []string) {
	cfg := parseToolSpec(spec)
	return cfg.Preset, cfg.Allow, cfg.Deny
}

// mergeToolPolicyEnv overlays GORTEX_TOOLS / GORTEX_TOOLS_MODE over a
// base (config-file / flag-folded) config: an env preset or mode
// overrides the base when set; allow/deny deltas append. Mirrors the
// repo-wide "GORTEX_* env overrides file config" convention.
func mergeToolPolicyEnv(base ToolPolicyConfig) ToolPolicyConfig {
	env, ok := toolPolicyConfigFromEnv()
	if !ok {
		return base
	}
	out := base
	if env.Preset != "" {
		out.Preset = env.Preset
	}
	if env.Mode != "" {
		out.Mode = env.Mode
	}
	out.Allow = append(append([]string{}, base.Allow...), env.Allow...)
	out.Deny = append(append([]string{}, base.Deny...), env.Deny...)
	return out
}

// resolveToolPolicy builds the policy from a base config (threaded from
// options / the config file) with the GORTEX_TOOLS / GORTEX_TOOLS_MODE
// env overrides applied on top.
func resolveToolPolicy(base ToolPolicyConfig, logger *zap.Logger) *toolPolicy {
	return newToolPolicy(mergeToolPolicyEnv(base), logger)
}

// toolPolicyBaseFromOptions extracts the config-supplied tool policy
// from the MultiRepoOptions, or the zero config when none was provided
// (the GORTEX_TOOLS env override still applies in resolveToolPolicy).
func toolPolicyBaseFromOptions(opts []MultiRepoOptions) ToolPolicyConfig {
	if len(opts) > 0 && opts[0].ToolPolicy != nil {
		return *opts[0].ToolPolicy
	}
	return ToolPolicyConfig{}
}
