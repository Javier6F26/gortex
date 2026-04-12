package main

import (
	"github.com/spf13/cobra"

	"github.com/zzet/gortex/internal/hooks"
)

var hookPort int

var hookCmd = &cobra.Command{
	Use:    "hook",
	Short:  "Claude Code hook handler (dispatches PreToolUse and PreCompact)",
	Hidden: true, // Not for direct user invocation.
	Run: func(_ *cobra.Command, _ []string) {
		hooks.Run(hookPort)
	},
}

func init() {
	hookCmd.Flags().IntVar(&hookPort, "port", 8765, "Gortex web server port")
	rootCmd.AddCommand(hookCmd)
}
