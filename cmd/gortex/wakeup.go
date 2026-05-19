// wakeup.go — `gortex wakeup` command. Emits the same ~500-token
// markdown digest the gortex_wakeup MCP tool produces, so users
// without an MCP transport (web ChatGPT, raw API, hosted Codex) can
// paste it into a chat session at startup.
//
// Implementation shares the renderer with the MCP path: both
// surfaces delegate to mcp.BuildWakeup so output stays
// byte-identical between transports.
package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/analysis"
	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/indexer"
	"github.com/zzet/gortex/internal/mcp"
	"github.com/zzet/gortex/internal/parser"
	"github.com/zzet/gortex/internal/parser/languages"
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
	Long: `Indexes the target repo and prints a paste-ready markdown digest
containing the language mix, top communities, load-bearing hotspots,
and entry points — capped at approximately --max-tokens (default
500).

Designed for users who can't run the MCP server: web ChatGPT, raw
API callers, hosted Codex. The MCP equivalent is the gortex_wakeup
tool; both share the renderer so output is byte-identical.

Examples:

  gortex wakeup                       # markdown to stdout, default budget
  gortex wakeup --max-tokens 800      # larger budget
  gortex wakeup --path /tmp/myrepo    # different tree`,
	RunE: runWakeup,
}

func init() {
	wakeupCmd.Flags().StringVar(&wakeupPath, "path", ".", "repository path to digest")
	wakeupCmd.Flags().IntVar(&wakeupMaxTokens, "max-tokens", 500, "approximate output token budget")
	wakeupCmd.Flags().IntVar(&wakeupTopCommunities, "top-communities", 4, "communities to include")
	wakeupCmd.Flags().IntVar(&wakeupTopHotspots, "top-hotspots", 5, "hotspots to include")
	wakeupCmd.Flags().IntVar(&wakeupTopEntries, "top-entry-points", 5, "entry points to include")
	rootCmd.AddCommand(wakeupCmd)
}

func runWakeup(cmd *cobra.Command, _ []string) error {
	abs, err := filepath.Abs(wakeupPath)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	g := graph.New()
	reg := parser.NewRegistry()
	languages.RegisterAll(reg)
	cfg := config.Config{}
	idx := indexer.New(g, reg, cfg.Index, zap.NewNop())
	fmt.Fprintf(cmd.ErrOrStderr(), "[wakeup] indexing %s...\n", abs)
	if _, err := idx.Index(abs); err != nil {
		return fmt.Errorf("index: %w", err)
	}

	// Community detection — same call as the daemon's RunAnalysis
	// path uses. Empty result is fine; the renderer skips that
	// section when there are no communities to surface.
	communities := analysis.DetectCommunities(g)

	md, _ := mcp.BuildWakeup(g, communities, mcp.WakeupOptions{
		MaxTokens:      wakeupMaxTokens,
		TopCommunities: wakeupTopCommunities,
		TopHotspots:    wakeupTopHotspots,
		TopEntryPoints: wakeupTopEntries,
	})

	if _, err := fmt.Fprint(cmd.OutOrStdout(), md); err != nil {
		return err
	}
	return nil
}
