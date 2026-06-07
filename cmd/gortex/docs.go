package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var (
	docsOut        string
	docsSince      time.Duration
	docsTop        int
	docsInclude    []string
	docsFormat     string
	docsPathPrefix string
	docsWorkspace  string
	docsIncludeRun bool
)

var docsCmd = &cobra.Command{
	Use:   "docs [path]",
	Short: "Generate a docs bundle (recent changes + ownership + stale code + blame)",
	Long: `Produce a markdown (or JSON) bundle of the four "living changelog"
sections: recent file changes, per-author ownership, stale code older than
365 days, and an on-demand blame re-run.

Runs generate_docs against the daemon that owns the repo; requires a
running daemon that tracks it. With --out the daemon writes the bundle to
the given path, otherwise it is printed to stdout.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runDocs,
}

func init() {
	docsCmd.Flags().StringVarP(&docsOut, "out", "o", "", `output path; empty or "-" → stdout`)
	docsCmd.Flags().DurationVar(&docsSince, "since", 7*24*time.Hour, "include recent changes within this window")
	docsCmd.Flags().IntVar(&docsTop, "top", 20, "cap each section's row count")
	docsCmd.Flags().StringSliceVar(&docsInclude, "include", []string{"recent", "ownership", "stale", "blame"},
		"sections to include (comma-separated)")
	docsCmd.Flags().StringVarP(&docsFormat, "format", "f", "markdown", "output format: markdown | json")
	docsCmd.Flags().StringVar(&docsPathPrefix, "path-prefix", "", "filter ownership/stale to this file prefix")
	docsCmd.Flags().StringVar(&docsWorkspace, "workspace", "", "restrict nodes to this WorkspaceID")
	docsCmd.Flags().BoolVar(&docsIncludeRun, "run-blame", false, "re-run git blame across the indexed repo before rendering")
	rootCmd.AddCommand(docsCmd)
}

func runDocs(cmd *cobra.Command, args []string) error {
	repoPath := "."
	if len(args) == 1 {
		repoPath = args[0]
	}

	toolArgs := map[string]any{
		"format":      docsFormat,
		"since":       docsSince.String(),
		"top":         docsTop,
		"include":     strings.Join(docsInclude, ","),
		"path_prefix": docsPathPrefix,
		"workspace":   docsWorkspace,
		"run_blame":   docsIncludeRun,
	}

	// The daemon writes the file, so it needs an absolute path (its cwd
	// is not the user's).
	writeToDisk := docsOut != "" && docsOut != "-"
	var absOut string
	if writeToDisk {
		a, err := filepath.Abs(docsOut)
		if err != nil {
			return fmt.Errorf("resolve output path: %w", err)
		}
		absOut = a
		toolArgs["output_path"] = absOut
	}

	out, err := requireDaemonTool(repoPath, "generate_docs", toolArgs)
	if err != nil {
		return err
	}
	if writeToDisk {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "[gortex docs] daemon wrote the bundle to %s\n", absOut)
		return nil
	}
	_, err = cmd.OutOrStdout().Write(out)
	return err
}
