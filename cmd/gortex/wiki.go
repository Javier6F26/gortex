package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/zzet/gortex/internal/wiki"
)

var (
	wikiOutputDir      string
	wikiFormat         string
	wikiWikilinks      bool
	wikiRepo           string
	wikiProject        string
	wikiWorkspace      string
	wikiMinCommunity   int
	wikiMaxCommunities int
	wikiNoProcesses    bool
	wikiNoContracts    bool
	wikiNoDocs         bool
	wikiEnhance        bool
	wikiForce          bool
)

var wikiCmd = &cobra.Command{
	Use:   "wiki [path]",
	Short: "Generate a markdown wiki of the indexed graph",
	Long: `Render a multi-page markdown wiki from the graph the daemon owns.

The wiki is template-driven (no LLM required). Pass --enhance to add
narrative summaries via the daemon's configured LLM provider. Output
layout:

  wiki/
    index.md                  # top-level repo index
    <repo>/
      index.md                # community navigation
      architecture.md         # system overview
      communities/...
      processes/...
      contracts/api-surface.md
      analysis/{hotspots,cycles,semantic}.md

Runs generate_wiki against the daemon that owns the repo (requires a
running daemon that tracks it); the daemon writes the files under the
resolved --output directory.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runWiki,
}

func init() {
	wikiCmd.Flags().StringVarP(&wikiOutputDir, "output", "o", "wiki", "output directory")
	wikiCmd.Flags().StringVarP(&wikiFormat, "format", "f", "markdown", "output format: markdown | html")
	wikiCmd.Flags().BoolVar(&wikiWikilinks, "wikilinks", false, "use [[wikilink]] style links (Obsidian-compatible)")
	wikiCmd.Flags().StringVar(&wikiRepo, "repo", "", "per-repo slug under wiki/ (default: basename of path)")
	wikiCmd.Flags().StringVar(&wikiProject, "project", "", "project label (multi-repo mode hint)")
	wikiCmd.Flags().StringVar(&wikiWorkspace, "workspace", "", "restrict emitted nodes to this WorkspaceID")
	wikiCmd.Flags().IntVar(&wikiMinCommunity, "min-community", 3, "minimum community size to document")
	wikiCmd.Flags().IntVar(&wikiMaxCommunities, "max-communities", 20, "max number of communities to document")
	wikiCmd.Flags().BoolVar(&wikiNoProcesses, "no-processes", false, "skip process pages")
	wikiCmd.Flags().BoolVar(&wikiNoContracts, "no-contracts", false, "skip contracts page")
	wikiCmd.Flags().BoolVar(&wikiNoDocs, "no-docs", false, "skip docs bundle (changelog/ownership/stale)")
	wikiCmd.Flags().BoolVar(&wikiEnhance, "enhance", false, "use the daemon's LLM provider to enrich narrative sections")
	wikiCmd.Flags().BoolVar(&wikiForce, "force", false, "suppress any 'already exists' diagnostics (writer is always idempotent)")
	rootCmd.AddCommand(wikiCmd)
}

func runWiki(cmd *cobra.Command, args []string) error {
	repoPath := "."
	if len(args) == 1 {
		repoPath = args[0]
	}

	// The daemon writes the files, so it needs an absolute output dir
	// (its cwd is not the user's).
	absOut, err := filepath.Abs(wikiOutputDir)
	if err != nil {
		return fmt.Errorf("resolve output dir: %w", err)
	}

	repoSlug := wikiRepo
	if repoSlug == "" {
		repoSlug = wiki.RepoSlugFromPath(repoPath)
	}

	out, err := requireDaemonTool(repoPath, "generate_wiki", map[string]any{
		"output_dir":      absOut,
		"format":          wikiFormat,
		"wikilinks":       wikiWikilinks,
		"repo":            repoSlug,
		"project":         wikiProject,
		"workspace":       wikiWorkspace,
		"min_community":   wikiMinCommunity,
		"max_communities": wikiMaxCommunities,
		"no_processes":    wikiNoProcesses,
		"no_contracts":    wikiNoContracts,
		"no_docs":         wikiNoDocs,
		"force":           wikiForce,
		"enhance":         wikiEnhance,
	})
	if err != nil {
		return err
	}

	var res struct {
		OutputDir string `json:"output_dir"`
		FileCount int    `json:"file_count"`
	}
	w := cmd.OutOrStdout()
	if json.Unmarshal(out, &res) == nil && res.OutputDir != "" {
		_, _ = fmt.Fprintf(w, "wiki generated: %d files under %s\n", res.FileCount, res.OutputDir)
		_, _ = fmt.Fprintf(w, "open: %s/%s/index.md\n", res.OutputDir, repoSlug)
		return nil
	}
	_, _ = w.Write(out)
	return nil
}
