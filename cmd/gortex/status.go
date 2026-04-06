package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/indexer"
	"github.com/zzet/gortex/internal/parser"
	"github.com/zzet/gortex/internal/parser/languages"
)

var statusIndex string

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show index status: node/edge counts, languages, and file breakdown",
	RunE:  runStatus,
}

func init() {
	statusCmd.Flags().StringVar(&statusIndex, "index", ".", "repository path to index")
	rootCmd.AddCommand(statusCmd)
}

func runStatus(_ *cobra.Command, _ []string) error {
	logger := newLogger()
	defer func() { _ = logger.Sync() }()

	cfg, err := config.Load(cfgFile)
	if err != nil {
		return err
	}

	g := graph.New()
	reg := parser.NewRegistry()
	languages.RegisterAll(reg)

	idx := indexer.New(g, reg, cfg.Index, logger)
	result, err := idx.Index(statusIndex)
	if err != nil {
		return fmt.Errorf("indexing failed: %w", err)
	}

	stats := g.Stats()

	_, _ = fmt.Fprintf(os.Stdout, "Repository:  %s\n", statusIndex)
	_, _ = fmt.Fprintf(os.Stdout, "Files:       %d\n", result.FileCount)
	_, _ = fmt.Fprintf(os.Stdout, "Nodes:       %d\n", stats.TotalNodes)
	_, _ = fmt.Fprintf(os.Stdout, "Edges:       %d\n", stats.TotalEdges)
	_, _ = fmt.Fprintf(os.Stdout, "Duration:    %dms\n\n", result.DurationMs)

	if len(stats.ByLanguage) > 0 {
		_, _ = fmt.Fprintln(os.Stdout, "Languages:")
		for lang, count := range stats.ByLanguage {
			_, _ = fmt.Fprintf(os.Stdout, "  %-14s %d nodes\n", lang, count)
		}
		_, _ = fmt.Fprintln(os.Stdout)
	}

	if len(stats.ByKind) > 0 {
		_, _ = fmt.Fprintln(os.Stdout, "By kind:")
		for kind, count := range stats.ByKind {
			_, _ = fmt.Fprintf(os.Stdout, "  %-14s %d\n", kind, count)
		}
	}

	return nil
}
