// bench.go — user-facing benchmark suite. Wraps the lower-level
// `gortex eval ...` substrate (recall / embedders / swebench / tokens)
// in a marketing-ready CLI shape: predictable defaults, two output
// formats (markdown + JSON), per-run artifacts under bench/results/,
// and a USD-per-model card on top of the token scorecard.
//
// `gortex bench` is the surface customers see; `gortex eval` remains
// the substrate with the long-tail flags. Both stay supported — the
// bench wrapper deliberately picks a narrow opinionated subset so
// the headline numbers stay comparable across runs.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zzet/gortex/internal/savings"
)

var (
	benchOutDir          string
	benchFormat          string
	benchTokensCases     string
	benchTokensTokenizer string
	benchRecallFixture   string
	benchRecallIndex     string
	benchRecallRankers   string
	benchAllResponsesDay int
)

var benchCmd = &cobra.Command{
	Use:   "bench",
	Short: "Run benchmark suite (recall + tokens + embedders + swebench) with USD-per-model savings",
	Long: `User-facing benchmark surface over the lower-level gortex eval
substrate. Each subcommand runs one bench dimension with opinionated
defaults that make headline numbers comparable across runs.

Subcommands:
  recall      — recall@1/5/20 + MRR per ranker (wraps gortex eval recall)
  tokens      — GCX1 vs JSON wire-format size + USD savings per model card
  embedders   — quality vs latency across embedder choices
  swebench    — SWE-bench harness (skips gracefully when data isn't local)
  all         — runs the three cheap benches, writes a consolidated artifact

Output defaults to markdown on stdout. Use --format json for machine-
readable, --out-dir DIR to persist per-run artifacts.

Examples:
  gortex bench tokens                              # one-line scorecard + USD card
  gortex bench recall --fixture bench/fixtures/retrieval.yaml
  gortex bench all --out-dir bench/results
`,
}

func init() {
	benchCmd.PersistentFlags().StringVar(&benchOutDir, "out-dir", "", "directory for per-run artifacts (default: stdout only)")
	benchCmd.PersistentFlags().StringVar(&benchFormat, "format", "markdown", "output format: markdown or json")
	rootCmd.AddCommand(benchCmd)

	benchCmd.AddCommand(benchRecallCmd)
	benchCmd.AddCommand(benchTokensCmd)
	benchCmd.AddCommand(benchEmbeddersCmd)
	benchCmd.AddCommand(benchSWECmd)
	benchCmd.AddCommand(benchAllCmd)

	benchRecallCmd.Flags().StringVar(&benchRecallFixture, "fixture", "bench/fixtures/retrieval.yaml", "fixture YAML path")
	benchRecallCmd.Flags().StringVar(&benchRecallIndex, "index", ".", "repository path to index")
	benchRecallCmd.Flags().StringVar(&benchRecallRankers, "rankers", "", "comma-separated subset of rankers (default: all)")

	benchTokensCmd.Flags().StringVar(&benchTokensCases, "cases", "bench/wire-format/cases", "directory of fixture YAML files")
	benchTokensCmd.Flags().StringVar(&benchTokensTokenizer, "tokenizer", "both", "cl100k | opus47 | both")

	benchAllCmd.Flags().IntVar(&benchAllResponsesDay, "responses-per-day", 1000, "responses/day used to scale the USD-per-model card")
	benchTokensCmd.Flags().IntVar(&benchAllResponsesDay, "responses-per-day", 1000, "responses/day used to scale the USD-per-model card (alias of the value used by `bench all`)")
}

// --- subcommand: recall ---------------------------------------------

var benchRecallCmd = &cobra.Command{
	Use:   "recall",
	Short: "Recall@k + MRR per ranker (wraps gortex eval recall)",
	RunE: func(_ *cobra.Command, _ []string) error {
		args := []string{
			"eval", "recall",
			"--fixture", benchRecallFixture,
			"--index", benchRecallIndex,
			"--format", benchFormat,
		}
		if benchRecallRankers != "" {
			args = append(args, "--rankers", benchRecallRankers)
		}
		outPath, err := outputPathFor("recall", benchFormat)
		if err != nil {
			return err
		}
		if outPath != "" {
			args = append(args, "--out", outPath)
		}
		return runGortexSubcommand(args...)
	},
}

// --- subcommand: tokens ---------------------------------------------

var benchTokensCmd = &cobra.Command{
	Use:   "tokens",
	Short: "GCX1 vs JSON wire-format scorecard + USD-per-model card",
	RunE: func(cmd *cobra.Command, _ []string) error {
		// Always capture machine-readable metrics so the USD card can
		// layer on top. Use a temp JSON sink when --out-dir is not
		// configured; otherwise write to the artifact directory.
		jsonPath, err := outputPathFor("tokens", "json")
		if err != nil {
			return err
		}
		var tmpJSON string
		if jsonPath == "" {
			f, err := os.CreateTemp("", "gortex-bench-tokens-*.json")
			if err != nil {
				return err
			}
			tmpJSON = f.Name()
			_ = f.Close()
			defer func() { _ = os.Remove(tmpJSON) }()
			jsonPath = tmpJSON
		}

		// Markdown scorecard goes either to stdout, an artifact, or
		// is discarded when format=json (caller wants the metrics
		// only, not the rendered table).
		scorecardPath, err := outputPathFor("tokens", "markdown")
		if err != nil {
			return err
		}

		args := []string{
			"run", "./bench/wire-format",
			"-cases", benchTokensCases,
			"-tokenizer", benchTokensTokenizer,
			"-json", jsonPath,
		}
		if scorecardPath != "" {
			args = append(args, "-out", scorecardPath)
		} else if benchFormat == "markdown" {
			// markdown on stdout — let the underlying tool print.
		} else {
			// format=json + no out-dir: redirect markdown to a temp
			// sink so it doesn't pollute the JSON output.
			f, err := os.CreateTemp("", "gortex-bench-tokens-*.md")
			if err != nil {
				return err
			}
			_ = f.Close()
			defer func() { _ = os.Remove(f.Name()) }()
			args = append(args, "-out", f.Name())
		}

		subproc := exec.Command("go", args...)
		subproc.Stdin = os.Stdin
		subproc.Stderr = os.Stderr
		if benchFormat == "markdown" && scorecardPath == "" {
			subproc.Stdout = os.Stdout
		}
		if err := subproc.Run(); err != nil {
			return fmt.Errorf("wire-format bench: %w", err)
		}

		// Load the metrics and emit the USD card.
		metrics, err := loadTokensMetrics(jsonPath)
		if err != nil {
			return fmt.Errorf("load tokens metrics: %w", err)
		}
		card := renderUSDCard(metrics, benchAllResponsesDay)

		switch benchFormat {
		case "markdown", "md":
			_, _ = fmt.Fprintln(cmd.OutOrStdout())
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), card)
		case "json":
			out, err := json.MarshalIndent(map[string]any{
				"metrics":   metrics,
				"usd_card":  buildUSDCardJSON(metrics, benchAllResponsesDay),
				"generated": time.Now().UTC().Format(time.RFC3339),
			}, "", "  ")
			if err != nil {
				return err
			}
			_, _ = cmd.OutOrStdout().Write(out)
			_, _ = fmt.Fprintln(cmd.OutOrStdout())
		default:
			return fmt.Errorf("unknown --format %q (want markdown or json)", benchFormat)
		}
		return nil
	},
}

// --- subcommand: embedders ------------------------------------------

var benchEmbeddersCmd = &cobra.Command{
	Use:   "embedders",
	Short: "Quality vs latency across embedder choices (wraps gortex eval embedders)",
	RunE: func(_ *cobra.Command, _ []string) error {
		args := []string{"eval", "embedders"}
		outPath, err := outputPathFor("embedders", benchFormat)
		if err != nil {
			return err
		}
		if outPath != "" {
			args = append(args, "--out", outPath)
		}
		return runGortexSubcommand(args...)
	},
}

// --- subcommand: swebench -------------------------------------------

var benchSWECmd = &cobra.Command{
	Use:   "swebench",
	Short: "SWE-bench harness (skips gracefully when data isn't local)",
	RunE: func(cmd *cobra.Command, _ []string) error {
		// Skip cleanly when the harness dependencies (Python + the
		// data set) aren't present; SWE-bench is multi-day GPU work
		// and we don't want CI / casual users to wait on a missing
		// dataset.
		if !swebenchAvailable() {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(),
				"[gortex bench swebench] SWE-bench harness not available locally;",
				"see eval/README.md for setup. Skipping.")
			return nil
		}
		return runGortexSubcommand("eval", "swebench")
	},
}

// --- subcommand: all ------------------------------------------------

var benchAllCmd = &cobra.Command{
	Use:   "all",
	Short: "Run the three cheap benches (recall + tokens + embedders) and write a consolidated artifact",
	RunE: func(cmd *cobra.Command, _ []string) error {
		dir := benchOutDir
		if dir == "" {
			dir = filepath.Join("bench", "results")
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		stamp := time.Now().UTC().Format("20060102-150405")
		runDir := filepath.Join(dir, "run-"+stamp)
		if err := os.MkdirAll(runDir, 0o755); err != nil {
			return err
		}

		// Run each sub-bench with its artifact directory pointed at
		// the run-specific subdir, so a single `bench all` produces
		// one tidy folder.
		previousOutDir := benchOutDir
		benchOutDir = runDir
		defer func() { benchOutDir = previousOutDir }()

		results := map[string]string{}
		for _, sub := range []struct {
			name    string
			runFn   func() error
		}{
			{"tokens", func() error { return benchTokensCmd.RunE(cmd, nil) }},
			{"recall", func() error { return benchRecallCmd.RunE(cmd, nil) }},
			{"embedders", func() error { return benchEmbeddersCmd.RunE(cmd, nil) }},
		} {
			if err := sub.runFn(); err != nil {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
					"[gortex bench all] %s failed: %v (continuing)\n", sub.name, err)
				results[sub.name] = "failed: " + err.Error()
				continue
			}
			results[sub.name] = "ok"
		}

		summary := map[string]any{
			"generated":          time.Now().UTC().Format(time.RFC3339),
			"run_dir":            runDir,
			"results":            results,
			"responses_per_day":  benchAllResponsesDay,
		}
		summaryBytes, _ := json.MarshalIndent(summary, "", "  ")
		if err := os.WriteFile(filepath.Join(runDir, "summary.json"), summaryBytes, 0o644); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\n[gortex bench all] artifacts: %s\n", runDir)
		return nil
	},
}

// --- helpers --------------------------------------------------------

// outputPathFor builds the artifact filename for a sub-bench when
// --out-dir is set. Returns "" when no path should be passed
// (substack defaults to stdout). The extension reflects the chosen
// format so a reader can tell at a glance which artifact is which.
func outputPathFor(bench, format string) (string, error) {
	if benchOutDir == "" {
		return "", nil
	}
	if err := os.MkdirAll(benchOutDir, 0o755); err != nil {
		return "", err
	}
	ext := "md"
	if format == "json" {
		ext = "json"
	}
	return filepath.Join(benchOutDir, bench+"."+ext), nil
}

// runGortexSubcommand re-execs the current binary with the provided
// args. Keeps state clean (no shared globals between sub-benches) and
// makes each invocation independently debuggable.
func runGortexSubcommand(args ...string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate gortex binary: %w", err)
	}
	subproc := exec.Command(self, args...)
	subproc.Stdin = os.Stdin
	subproc.Stdout = os.Stdout
	subproc.Stderr = os.Stderr
	return subproc.Run()
}

// swebenchAvailable reports whether the SWE-bench harness can be
// reasonably expected to run. Conservative: requires Python on PATH
// AND the eval/ directory in the repo (which carries the harness).
func swebenchAvailable() bool {
	if _, err := exec.LookPath("python3"); err != nil {
		if _, err := exec.LookPath("python"); err != nil {
			return false
		}
	}
	if st, err := os.Stat("eval"); err != nil || !st.IsDir() {
		return false
	}
	return true
}

// --- tokens-bench metrics + USD card --------------------------------

// tokensMetric mirrors the on-disk row shape emitted by the
// bench/wire-format harness. We only consume the fields the USD card
// needs; extra fields in the JSON are ignored.
type tokensMetric struct {
	Case             string `json:"Case"`
	Tool             string `json:"Tool"`
	JSONTokens       int    `json:"JSONTokens"`
	GCXTokens        int    `json:"GCXTokens"`
	JSONTokensOpus47 int    `json:"JSONTokensOpus47,omitempty"`
	GCXTokensOpus47  int    `json:"GCXTokensOpus47,omitempty"`
}

func loadTokensMetrics(path string) ([]tokensMetric, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	var rows []tokensMetric
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// medianSavedTokens returns the median per-response token savings —
// JSON tokens minus GCX tokens — across the supplied metrics. Returns
// 0 when no rows or no valid savings (defensive against pathological
// inputs that would zero out the USD card).
func medianSavedTokens(rows []tokensMetric, useOpus47 bool) int {
	if len(rows) == 0 {
		return 0
	}
	deltas := make([]int, 0, len(rows))
	for _, r := range rows {
		var saved int
		if useOpus47 && r.JSONTokensOpus47 > 0 {
			saved = r.JSONTokensOpus47 - r.GCXTokensOpus47
		} else if r.JSONTokens > 0 {
			saved = r.JSONTokens - r.GCXTokens
		}
		if saved > 0 {
			deltas = append(deltas, saved)
		}
	}
	if len(deltas) == 0 {
		return 0
	}
	sort.Ints(deltas)
	return deltas[len(deltas)/2]
}

// renderUSDCard produces a markdown table projecting per-day and
// per-month savings at each model's input-token rate. responsesPerDay
// scales the per-response figure into the headline numbers a buyer
// will share. Pricing comes from internal/savings — overrideable via
// GORTEX_MODEL_PRICING_JSON for org-specific rates.
func renderUSDCard(rows []tokensMetric, responsesPerDay int) string {
	if responsesPerDay <= 0 {
		responsesPerDay = 1000
	}
	medianCL := medianSavedTokens(rows, false)
	medianOpus := medianSavedTokens(rows, true)

	var b strings.Builder
	fmt.Fprintln(&b, "## USD savings projection (per the bench median)")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Median tokens saved / response: **%d** (cl100k_base)", medianCL)
	if medianOpus > 0 {
		fmt.Fprintf(&b, ", **%d** (Opus 4.7)", medianOpus)
	}
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Projected at %d responses/day:\n\n", responsesPerDay)
	fmt.Fprintln(&b, "| Model            | $/M input | $/day  | $/month |")
	fmt.Fprintln(&b, "|------------------|----------:|-------:|--------:|")

	prices := savings.Pricing()
	for _, p := range prices {
		// Use Opus 4.7 figures for Claude Opus 4.x (a closer match
		// for that family's tokenizer); cl100k_base everywhere else.
		median := medianCL
		if medianOpus > 0 && strings.Contains(strings.ToLower(p.Model), "opus") {
			median = medianOpus
		}
		perResponse := float64(median) / 1_000_000.0 * p.USDPerMInput
		perDay := perResponse * float64(responsesPerDay)
		perMonth := perDay * 30.0
		fmt.Fprintf(&b, "| %-16s | $%-8.2f | $%-5.2f | $%-7.2f |\n",
			p.Model, p.USDPerMInput, perDay, perMonth)
	}
	return b.String()
}

// buildUSDCardJSON returns the same data the markdown card surfaces,
// in structured form for --format=json consumers.
func buildUSDCardJSON(rows []tokensMetric, responsesPerDay int) map[string]any {
	if responsesPerDay <= 0 {
		responsesPerDay = 1000
	}
	medianCL := medianSavedTokens(rows, false)
	medianOpus := medianSavedTokens(rows, true)

	models := make([]map[string]any, 0)
	for _, p := range savings.Pricing() {
		median := medianCL
		if medianOpus > 0 && strings.Contains(strings.ToLower(p.Model), "opus") {
			median = medianOpus
		}
		perResponse := float64(median) / 1_000_000.0 * p.USDPerMInput
		models = append(models, map[string]any{
			"model":          p.Model,
			"usd_per_m":      p.USDPerMInput,
			"usd_per_resp":   perResponse,
			"usd_per_day":    perResponse * float64(responsesPerDay),
			"usd_per_month":  perResponse * float64(responsesPerDay) * 30.0,
		})
	}
	return map[string]any{
		"median_saved_tokens_cl100k": medianCL,
		"median_saved_tokens_opus47": medianOpus,
		"responses_per_day":          responsesPerDay,
		"models":                     models,
	}
}
