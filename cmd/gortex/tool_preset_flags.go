package main

import (
	"github.com/zzet/gortex/internal/config"
	gortexmcp "github.com/zzet/gortex/internal/mcp"
)

// applyToolPresetFlags folds the --tools / --tools-mode flags into the
// loaded config's mcp.tools block before the server stack reads it
// (flag overrides config file). GORTEX_TOOLS / GORTEX_TOOLS_MODE still
// override at server construction, so the effective precedence is
// env > flag > config > default(full).
func applyToolPresetFlags(cfg *config.Config, flagTools, flagMode string) {
	if cfg == nil {
		return
	}
	if flagTools != "" {
		preset, allow, deny := gortexmcp.ParseToolSpec(flagTools)
		if preset != "" {
			cfg.MCP.Tools.Preset = preset
		}
		cfg.MCP.Tools.Allow = append(cfg.MCP.Tools.Allow, allow...)
		cfg.MCP.Tools.Deny = append(cfg.MCP.Tools.Deny, deny...)
	}
	if flagMode != "" {
		cfg.MCP.Tools.Mode = flagMode
	}
}
