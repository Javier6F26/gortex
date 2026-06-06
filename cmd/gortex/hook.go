package main

import (
	"github.com/spf13/cobra"

	"github.com/zzet/gortex/internal/hooks"
)

var (
	hookPort  int
	hookMode  string
	hookAgent string
)

var hookCmd = &cobra.Command{
	Use:    "hook",
	Short:  "Agent hook handler (Claude Code by default; --agent for Gemini / Antigravity)",
	Hidden: true, // Not for direct user invocation.
	Run: func(_ *cobra.Command, _ []string) {
		// A non-Claude agent (Gemini CLI, Antigravity) shares the
		// hookSpecificOutput.additionalContext wire shape but a different
		// event vocabulary, so it routes to the external-agent handler.
		if hookAgent != "" {
			hooks.RunExternalAgent()
			return
		}
		hooks.Run(hookPort, hooks.ParseMode(hookMode))
	},
}

func init() {
	hookCmd.Flags().IntVar(&hookPort, "port", 8765, "Gortex web server port")
	hookCmd.Flags().StringVar(&hookMode, "mode", "deny",
		"hook posture: 'deny' (redirect Grep/Glob/Read of indexed source), 'enrich' (never deny; PostToolUse appends graph context), 'consult-unlock' (deny fallback reads until the graph is queried once this session), or 'nudge' (soft-deny once per burst of non-symbolic calls)")
	hookCmd.Flags().StringVar(&hookAgent, "agent", "",
		"non-Claude agent hook handler: 'gemini' or 'antigravity' (emits hookSpecificOutput.additionalContext). Default (empty) is the Claude Code format.")
	rootCmd.AddCommand(hookCmd)
}
