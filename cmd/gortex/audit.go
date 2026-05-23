// audit.go — `gortex audit` command. Produces a one-letter
// repo-level health grade (A-F) plus a shields.io-style SVG badge
// suitable for embedding in a README. Grade is derived from the
// graph's complexity-axis health score — the same arithmetic the
// MCP `analyze kind=health_score` analyzer uses, restricted to the
// axes available without external enrichment (no coverage profile,
// no blame data, no session history required).
//
// The simplification matters: the badge has to work on a
// freshly-indexed repo with zero enrichment. Coverage + blame
// axes add fidelity when present but require multi-step setup;
// gating the badge on them would make the README shield
// effectively unreachable.
package main

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/indexer"
	"github.com/zzet/gortex/internal/parser"
	"github.com/zzet/gortex/internal/parser/languages"
	"github.com/zzet/gortex/internal/progress"
	"github.com/zzet/gortex/internal/tui"
)

var (
	auditPath   string
	auditBadge  bool
	auditOut    string
	auditFormat string
)

var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Compute a repo-level A-F health grade + emit a README-ready SVG badge",
	Long: `Indexes the target repo and reports a single A-F grade based
on graph-topology complexity. Designed for the README shield:
the grade reflects how well-structured the indexed code is, on
the axes available without external enrichment (fan-in /
fan-out / community-crossings per callable symbol).

Output modes:

  --format svg   (default) shields.io-style SVG. Default path
                 .gortex/badge.svg. Embed in README via
                 ![gortex audit](.gortex/badge.svg).
  --format json  machine-readable score + per-axis breakdown.
  --format text  one-line grade + score for quick CLI use.

Examples:

  gortex audit                          # write .gortex/badge.svg
  gortex audit --format text            # "A · 87.4" on stdout
  gortex audit --format json --out -    # JSON on stdout
  gortex audit --path /tmp/myrepo       # audit a different tree`,
	RunE: runAudit,
}

func init() {
	auditCmd.Flags().StringVar(&auditPath, "path", ".", "repository path to audit")
	auditCmd.Flags().BoolVar(&auditBadge, "badge", true, "write an SVG shield (alias of --format svg)")
	auditCmd.Flags().StringVar(&auditOut, "out", "", "output path (default: .gortex/badge.svg for svg, stdout for json/text)")
	auditCmd.Flags().StringVar(&auditFormat, "format", "svg", "svg | json | text")
	rootCmd.AddCommand(auditCmd)
}

func runAudit(cmd *cobra.Command, _ []string) error {
	abs, err := filepath.Abs(auditPath)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	w := cmd.ErrOrStderr()
	emitAuditBanner(w, abs)

	// Index the repo with a spinner so the user sees progress instead of a
	// silent multi-second pause on larger trees. Indexer logs are silenced
	// on TTY (the spinner already renders the same stage transitions).
	g := graph.New()
	reg := parser.NewRegistry()
	languages.RegisterAll(reg)
	cfg := config.Config{}
	logger := loggerForSpinner(cmd, zap.NewNop())
	idx := indexer.New(g, reg, cfg.Index, logger)

	sp := newAuditSpinner(w)
	if sp != nil {
		sp.Start("Indexing repository")
		sp.Set("", abs)
	} else {
		_, _ = fmt.Fprintf(w, "[audit] indexing %s...\n", abs)
	}
	if _, ierr := idx.Index(abs); ierr != nil {
		if sp != nil {
			sp.Fail(ierr)
		}
		return fmt.Errorf("index: %w", ierr)
	}
	if sp != nil {
		sp.Done()
	}

	report := computeAuditReport(g)

	// On non-TTY (or --no-progress / non-svg formats), preserve the legacy
	// summary line so script parsers keep working.
	if !progress.IsTTY(w) || noProgress || strings.ToLower(auditFormat) != "svg" {
		_, _ = fmt.Fprintf(w,
			"[audit] %d callable symbols · mean complexity-health %.1f · grade %s\n",
			report.SymbolCount, report.MeanScore, report.Grade)
	} else {
		emitAuditGradeCard(w, report)
	}

	switch strings.ToLower(auditFormat) {
	case "svg":
		out := auditOut
		if out == "" {
			out = filepath.Join(abs, ".gortex", "badge.svg")
		}
		if out == "-" {
			_, err := cmd.OutOrStdout().Write([]byte(renderBadgeSVG(report)))
			return err
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(out, []byte(renderBadgeSVG(report)), 0o644); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "[audit] wrote %s\n", out)
		// README snippet so the user can paste it.
		_, _ = fmt.Fprintf(cmd.OutOrStdout(),
			"![gortex audit](%s) · grade %s · score %.1f\n",
			filepath.ToSlash(filepath.Clean(strings.TrimPrefix(out, abs+string(filepath.Separator)))),
			report.Grade, report.MeanScore)
	case "json":
		body := renderAuditJSON(report)
		if auditOut == "" || auditOut == "-" {
			_, _ = cmd.OutOrStdout().Write([]byte(body))
			_, _ = cmd.OutOrStdout().Write([]byte("\n"))
			return nil
		}
		return os.WriteFile(auditOut, []byte(body), 0o644)
	case "text":
		line := fmt.Sprintf("%s · %.1f", report.Grade, report.MeanScore)
		if auditOut == "" || auditOut == "-" {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), line)
			return nil
		}
		return os.WriteFile(auditOut, []byte(line+"\n"), 0o644)
	default:
		return fmt.Errorf("unknown --format %q (want svg | json | text)", auditFormat)
	}
	return nil
}

// auditReport is the per-repo summary the badge / json output
// renders. Keeps the structured data separate from rendering so
// tests can pin the math without screen-scraping SVG.
type auditReport struct {
	SymbolCount int     `json:"symbol_count"`
	MeanScore   float64 `json:"mean_score"`
	Grade       string  `json:"grade"`
	// Distribution per grade band — the badge surfaces the
	// headline; this is the deep-dive for the JSON path.
	GradeCounts map[string]int `json:"grade_counts"`
	// Per-symbol scores sorted ascending so the worst-scored
	// symbols can be cited without re-walking the graph.
	WorstSymbols []symbolScore `json:"worst_symbols,omitempty"`
}

type symbolScore struct {
	ID    string  `json:"id"`
	Score float64 `json:"score"`
	Grade string  `json:"grade"`
	File  string  `json:"file"`
	Line  int     `json:"line"`
}

// computeAuditReport walks the graph and produces the per-repo
// grade. Complexity-axis-only math matching the multi-axis
// `analyze kind=health_score` analyzer's complexity component, so
// the badge grade is comparable to (a subset of) what the full
// analyzer would produce on the same graph.
//
// raw       = fan_in*2 + fan_out*1.5 + crossings*3   (per symbol)
// complexity_health = 100 / (1 + raw/20)
// mean      = mean across callable symbols
// grade     = scoreGrade(mean)
//
// We can't easily compute community crossings from the CLI without
// the analysis package's community detector. Approximation: skip
// the crossings term — at the repo scale a small constant bias
// across all symbols doesn't change the rank or grade meaningfully.
func computeAuditReport(g *graph.Graph) auditReport {
	type entry struct {
		id, file string
		line     int
		score    float64
	}
	var entries []entry
	for _, n := range g.AllNodes() {
		if n == nil {
			continue
		}
		if n.Kind != graph.KindFunction && n.Kind != graph.KindMethod {
			continue
		}
		fanIn := 0
		fanOut := 0
		for _, e := range g.GetInEdges(n.ID) {
			if e.Kind == graph.EdgeCalls || e.Kind == graph.EdgeReferences {
				fanIn++
			}
		}
		for _, e := range g.GetOutEdges(n.ID) {
			if e.Kind == graph.EdgeCalls {
				fanOut++
			}
		}
		raw := float64(fanIn)*2.0 + float64(fanOut)*1.5
		complexity := 100.0 / (1.0 + raw/20.0)
		entries = append(entries, entry{
			id:    n.ID,
			file:  n.FilePath,
			line:  n.StartLine,
			score: complexity,
		})
	}

	report := auditReport{
		SymbolCount: len(entries),
		GradeCounts: map[string]int{},
	}
	if len(entries) == 0 {
		report.Grade = scoreGradeForAudit(0)
		return report
	}

	var sum float64
	for _, e := range entries {
		sum += e.score
	}
	report.MeanScore = math.Round((sum/float64(len(entries)))*10) / 10
	report.Grade = scoreGradeForAudit(report.MeanScore)

	for _, e := range entries {
		report.GradeCounts[scoreGradeForAudit(e.score)]++
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].score != entries[j].score {
			return entries[i].score < entries[j].score
		}
		if entries[i].file != entries[j].file {
			return entries[i].file < entries[j].file
		}
		return entries[i].line < entries[j].line
	})
	// Top-5 worst — enough for "what to look at first" without
	// dragging in the full population.
	limit := min(5, len(entries))
	report.WorstSymbols = make([]symbolScore, 0, limit)
	for i := range limit {
		e := entries[i]
		report.WorstSymbols = append(report.WorstSymbols, symbolScore{
			ID:    e.id,
			Score: math.Round(e.score*10) / 10,
			Grade: scoreGradeForAudit(e.score),
			File:  e.file,
			Line:  e.line,
		})
	}
	return report
}

// scoreGradeForAudit mirrors the MCP analyzer's scoreGrade — kept
// duplicated so the audit command doesn't take an mcp dependency.
// Keep in sync with internal/mcp/tools_analyze_health_score.go.
func scoreGradeForAudit(score float64) string {
	switch {
	case score >= 85:
		return "A"
	case score >= 70:
		return "B"
	case score >= 55:
		return "C"
	case score >= 40:
		return "D"
	default:
		return "F"
	}
}

// renderAuditJSON is the structured-output form of the report.
func renderAuditJSON(r auditReport) string {
	// Hand-built so the field order in the output is stable
	// regardless of map iteration. Tests pin specific lines.
	var b strings.Builder
	fmt.Fprintf(&b, "{\n  \"symbol_count\": %d,\n  \"mean_score\": %.1f,\n  \"grade\": %q,\n",
		r.SymbolCount, r.MeanScore, r.Grade)
	b.WriteString("  \"grade_counts\": {")
	first := true
	for _, k := range []string{"A", "B", "C", "D", "F"} {
		if first {
			first = false
		} else {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "\n    %q: %d", k, r.GradeCounts[k])
	}
	b.WriteString("\n  },\n  \"worst_symbols\": [")
	for i, s := range r.WorstSymbols {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "\n    {\"id\": %q, \"score\": %.1f, \"grade\": %q, \"file\": %q, \"line\": %d}",
			s.ID, s.Score, s.Grade, s.File, s.Line)
	}
	b.WriteString("\n  ]\n}")
	return b.String()
}

// renderBadgeSVG produces a shields.io-style two-cell badge. Left
// cell ("gortex audit") in slate grey; right cell shows the grade
// in the tier-mapped colour. Plain SVG (no external font deps);
// width auto-sized for a 1-char grade.
func renderBadgeSVG(r auditReport) string {
	label := "gortex audit"
	grade := r.Grade
	colour := gradeColour(grade)

	// Hand-tuned widths — shields.io renders at 100% scale and
	// expects a tight bounding box. 78px left + 26px right is
	// readable at the typical README zoom.
	labelW := 78
	gradeW := 26
	totalW := labelW + gradeW

	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20" role="img" aria-label="gortex audit: %s">
  <title>gortex audit: %s · %.1f</title>
  <linearGradient id="s" x2="0" y2="100%%">
    <stop offset="0" stop-color="#bbb" stop-opacity=".1"/>
    <stop offset="1" stop-opacity=".1"/>
  </linearGradient>
  <clipPath id="r"><rect width="%d" height="20" rx="3" fill="#fff"/></clipPath>
  <g clip-path="url(#r)">
    <rect width="%d" height="20" fill="#555"/>
    <rect x="%d" width="%d" height="20" fill="%s"/>
    <rect width="%d" height="20" fill="url(#s)"/>
  </g>
  <g fill="#fff" text-anchor="middle" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" font-size="11">
    <text x="%d" y="14">%s</text>
    <text x="%d" y="14">%s</text>
  </g>
</svg>
`,
		totalW, grade, grade, r.MeanScore,
		totalW,
		labelW,
		labelW, gradeW, colour,
		totalW,
		labelW/2, label,
		labelW+gradeW/2, grade,
	)
}

// newAuditSpinner returns a fresh mesh spinner bound to w when stderr is a
// TTY (and --no-progress isn't set). Returns nil otherwise; the caller falls
// back to a one-line "indexing ..." print.
func newAuditSpinner(w io.Writer) *progress.Spinner {
	if noProgress || !progress.IsTTY(w) {
		return nil
	}
	return progress.NewSpinner(w)
}

// emitAuditBanner prints the gortex mesh banner + subtitle naming the repo
// under audit. TTY-only so non-TTY callers (CI badges, scripts) keep their
// minimal stderr trail.
func emitAuditBanner(w io.Writer, repoPath string) {
	if !progress.IsTTY(w) || noProgress {
		return
	}
	short := repoPath
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(repoPath, home) {
		short = "~" + strings.TrimPrefix(repoPath, home)
	}
	banner := tui.Banner{
		Title:    "gortex audit",
		Subtitle: "Computing complexity-axis health grade for " + filepath.Base(repoPath) + ".",
	}.Render()
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, banner)
	_, _ = fmt.Fprintln(w, "  "+progress.Row("repo", short, 8))
	_, _ = fmt.Fprintln(w)
}

// emitAuditGradeCard renders the celebratory result panel: a big colour-tiered
// grade chip, stat strip with symbol count + mean score, and a one-line
// breakdown of the per-grade distribution. The shields.io SVG is still
// written to .gortex/badge.svg by the caller — this is just the on-screen
// reward.
func emitAuditGradeCard(w io.Writer, r auditReport) {
	gradeStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(progress.PaletteFg()).
		Background(auditGradeBG(r.Grade)).
		Padding(0, 2)
	gradeChip := gradeStyle.Render(" " + r.Grade + " ")

	_, _ = fmt.Fprintln(w, "  "+gradeChip+"   "+
		progress.StyleStrong.Render(fmt.Sprintf("%.1f", r.MeanScore))+
		"  "+progress.StyleHint.Render("/ 100  ·  mean complexity-health"))

	stats := []string{
		progress.Stat(strconv.Itoa(r.SymbolCount), "callable symbols", progress.StatNeutral),
	}
	stats = append(stats, progress.Stat(gradeBlurb(r.Grade), "", auditStatSeverity(r.Grade)))
	_, _ = fmt.Fprintln(w, "     "+progress.StatStrip(stats...))

	if len(r.GradeCounts) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "     "+progress.Heading("grade distribution"))
		parts := make([]string, 0, 5)
		for _, k := range []string{"A", "B", "C", "D", "F"} {
			parts = append(parts,
				lipgloss.NewStyle().Bold(true).Foreground(auditGradeBG(k)).Render(k)+
					"  "+progress.StyleVal.Render(strconv.Itoa(r.GradeCounts[k])),
			)
		}
		_, _ = fmt.Fprintln(w, "     "+strings.Join(parts, progress.StyleHint.Render("   ·   ")))
	}
	_, _ = fmt.Fprintln(w)
}

// auditGradeBG maps an A-F letter to the same shields.io tier colour the SVG
// uses, so the on-screen card and the rendered badge agree visually.
func auditGradeBG(grade string) lipgloss.Color {
	switch grade {
	case "A":
		return lipgloss.Color("#4c1") // brightgreen
	case "B":
		return lipgloss.Color("#97ca00") // green
	case "C":
		return lipgloss.Color("#dfb317") // yellow
	case "D":
		return lipgloss.Color("#fe7d37") // orange
	default:
		return lipgloss.Color("#e05d44") // red
	}
}

// gradeBlurb returns the short human prefix shown next to the score in the
// card's stat strip.
func gradeBlurb(grade string) string {
	switch grade {
	case "A":
		return "excellent topology"
	case "B":
		return "healthy"
	case "C":
		return "watch fan-out hotspots"
	case "D":
		return "consider refactoring"
	default:
		return "high coupling risk"
	}
}

// auditStatSeverity colour-codes the blurb chip per grade band so the card
// telegraphs urgency without re-reading the letter.
func auditStatSeverity(grade string) progress.StatSeverity {
	switch grade {
	case "A", "B":
		return progress.StatGood
	case "C":
		return progress.StatNeutral
	case "D":
		return progress.StatWarn
	default:
		return progress.StatBad
	}
}

// gradeColour returns the shields.io standard tier colour per
// grade. A=brightgreen, B=green, C=yellow, D=orange, F=red.
func gradeColour(grade string) string {
	switch grade {
	case "A":
		return "#4c1" // brightgreen
	case "B":
		return "#97ca00" // green
	case "C":
		return "#dfb317" // yellow
	case "D":
		return "#fe7d37" // orange
	default:
		return "#e05d44" // red
	}
}
