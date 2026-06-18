package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/eval/parity"
)

var (
	parityCacheDir string
	parityUpdate   bool
	parityLang     string
	parityEpsilon  float64
)

var evalParityCmd = &cobra.Command{
	Use:   "parity",
	Short: "Measure per-language cross-file coverage on benchmark repos and check it against the committed baseline",
	Long: `Clone (and cache) the per-language benchmark corpus, index each repo, and
measure the share of symbol-bearing source files that have at least one resolved
cross-file dependent.

Default (assert) mode compares the measured coverage against the committed
baseline (internal/eval/parity/baseline.json) and exits non-zero if any language
regressed. --update prints the measured coverage as a baseline document instead;
redirect it into baseline.json and commit to capture or refresh the baseline.

  gortex eval parity                       # assert against the committed baseline
  gortex eval parity --lang go             # one language only
  gortex eval parity --update > internal/eval/parity/baseline.json
`,
	RunE: runEvalParity,
}

func init() {
	evalParityCmd.Flags().StringVar(&parityCacheDir, "cache-dir", "", "where benchmark repos are cloned/cached (default: a parity-cache dir under the OS temp dir)")
	evalParityCmd.Flags().BoolVar(&parityUpdate, "update", false, "print measured coverage as a baseline JSON document instead of asserting")
	evalParityCmd.Flags().StringVar(&parityLang, "lang", "", "only run the benchmark repo for this language")
	evalParityCmd.Flags().Float64Var(&parityEpsilon, "epsilon", 0.005, "tolerance below baseline before a language counts as a regression")
	evalCmd.AddCommand(evalParityCmd)
}

func runEvalParity(cmd *cobra.Command, args []string) error {
	cacheDir := parityCacheDir
	if cacheDir == "" {
		cacheDir = filepath.Join(os.TempDir(), "gortex-parity-cache")
	}
	ctx := context.Background()

	var measured []parity.LanguageCoverage
	for _, repo := range parity.BenchRepos() {
		if parityLang != "" && repo.Language != parityLang {
			continue
		}
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "[gortex eval parity] %-10s %s\n", repo.Language, repo.URL)
		dir, err := parity.EnsureRepo(cacheDir, repo)
		if err != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "  clone failed: %v — skipping\n", err)
			continue
		}
		g, err := indexRepoForInit(ctx, dir, zap.NewNop())
		if err != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "  index failed: %v — skipping\n", err)
			continue
		}
		for _, c := range parity.CoverageOf(g) {
			if c.Language != repo.Language {
				continue // a Go repo may carry a few yaml/json files; measure its own language
			}
			measured = append(measured, c)
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "  coverage %.1f%% (%d/%d files)\n", c.Coverage*100, c.CoveredFiles, c.SymbolFiles)
		}
	}

	if parityUpdate {
		out, err := parity.MarshalBaseline(measured)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(out))
		return nil
	}

	baseline, err := parity.LoadBaseline()
	if err != nil {
		return err
	}
	if len(baseline) == 0 {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "[gortex eval parity] no committed baseline yet — run with --update and commit the output to internal/eval/parity/baseline.json")
		return nil
	}
	regs := baseline.Check(measured, parityEpsilon)
	for _, r := range regs {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "[gortex eval parity] REGRESSION %s: %.1f%% < baseline %.1f%%\n", r.Language, r.Measured*100, r.Baseline*100)
	}
	if len(regs) > 0 {
		return fmt.Errorf("%d language(s) regressed below the parity baseline", len(regs))
	}
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "[gortex eval parity] all measured languages hold or beat the baseline")
	return nil
}
