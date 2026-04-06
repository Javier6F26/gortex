package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/zzet/gortex/internal/claudemd"
	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/indexer"
	"github.com/zzet/gortex/internal/parser"
	"github.com/zzet/gortex/internal/parser/languages"
	"github.com/zzet/gortex/internal/query"
)

var (
	claudeMdMaxLines int
	claudeMdAppend   bool
	claudeMdFile     string
	claudeMdIndex    string
)

var claudeMdCmd = &cobra.Command{
	Use:   "claude-md",
	Short: "Generate a CLAUDE.md block describing the codebase and Gortex tools",
	RunE:  runClaudeMd,
}

func init() {
	claudeMdCmd.Flags().IntVar(&claudeMdMaxLines, "max-lines", 180, "target maximum lines for generated block")
	claudeMdCmd.Flags().BoolVar(&claudeMdAppend, "append", false, "append to existing CLAUDE.md")
	claudeMdCmd.Flags().StringVar(&claudeMdFile, "file", "./CLAUDE.md", "target CLAUDE.md path")
	claudeMdCmd.Flags().StringVar(&claudeMdIndex, "index", ".", "repository path to index")
	rootCmd.AddCommand(claudeMdCmd)
}

func runClaudeMd(cmd *cobra.Command, args []string) error {
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
	if _, err := idx.Index(claudeMdIndex); err != nil {
		return err
	}

	eng := query.NewEngine(g)
	content := claudemd.Generate(eng, claudeMdMaxLines)

	if claudeMdAppend {
		f, err := os.OpenFile(claudeMdFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		_, err = f.WriteString("\n" + content)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Appended to %s\n", claudeMdFile)
	} else {
		fmt.Print(content)
	}
	return nil
}
