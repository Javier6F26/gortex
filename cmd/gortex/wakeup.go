// wakeup.go — `gortex wakeup` command. Emits the same ~500-token
// markdown digest the gortex_wakeup MCP tool produces, so users
// without an MCP transport (web ChatGPT, raw API, hosted Codex) can
// paste it into a chat session at startup. Routes through the daemon
// that owns the repo so the digest reflects the warm graph.
package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	wakeupPath           string
	wakeupMaxTokens      int
	wakeupTopCommunities int
	wakeupTopHotspots    int
	wakeupTopEntries     int
)

var wakeupCmd = &cobra.Command{
	Use:   "wakeup",
	Short: "Emit a ~500-token markdown codebase digest (for paste-into-chat use)",
	Long: `Prints a paste-ready markdown digest of the repo the daemon owns:
the language mix, top communities, load-bearing hotspots, and entry
points — capped at approximately --max-tokens (default 500).

Designed for users who can't run the MCP server: web ChatGPT, raw API
callers, hosted Codex. Routes through the daemon that tracks the repo
(requires a running daemon).

Examples:

  gortex wakeup                       # markdown to stdout, default budget
  gortex wakeup --max-tokens 800      # larger budget
  gortex wakeup --path /tmp/myrepo    # a tracked tree`,
	RunE: runWakeup,
}

func init() {
	wakeupCmd.Flags().StringVar(&wakeupPath, "path", ".", "repository path to digest (the daemon must track it)")
	wakeupCmd.Flags().IntVar(&wakeupMaxTokens, "max-tokens", 500, "approximate output token budget")
	wakeupCmd.Flags().IntVar(&wakeupTopCommunities, "top-communities", 4, "communities to include")
	wakeupCmd.Flags().IntVar(&wakeupTopHotspots, "top-hotspots", 5, "hotspots to include")
	wakeupCmd.Flags().IntVar(&wakeupTopEntries, "top-entry-points", 5, "entry points to include")
	rootCmd.AddCommand(wakeupCmd)
}

func runWakeup(cmd *cobra.Command, _ []string) error {
	out, err := requireDaemonTool(wakeupPath, "gortex_wakeup", map[string]any{
		"max_tokens":       wakeupMaxTokens,
		"top_communities":  wakeupTopCommunities,
		"top_hotspots":     wakeupTopHotspots,
		"top_entry_points": wakeupTopEntries,
		"format":           "markdown",
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(cmd.OutOrStdout(), string(out))
	return err
}
