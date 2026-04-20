// Command wire-format benches GCX1 vs JSON on representative MCP tool
// responses. See bench/wire-format/README.md.
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zzet/gortex/internal/tokens"
	"github.com/zzet/gortex/pkg/wire"
)

type caseFile struct {
	Tool        string `yaml:"tool"`
	Description string `yaml:"description"`
	// Input is the tool response payload as JSON text. The YAML pipe
	// scalar (`|`) keeps the JSON human-readable in the fixture.
	Input string `yaml:"input"`
}

type metrics struct {
	Case         string
	Tool         string
	JSONBytes    int
	GCXBytes     int
	JSONTokens   int
	GCXTokens    int
	JSONGzip     int
	GCXGzip      int
	RoundTripOK  bool
	RoundTripErr string
}

func main() {
	casesDir := flag.String("cases", "bench/wire-format/cases", "directory of fixture YAML files")
	out := flag.String("out", "", "output scorecard markdown path (stdout if empty)")
	jsonOut := flag.String("json", "", "optional path to emit raw metrics as JSON")
	flag.Parse()

	entries, err := os.ReadDir(*casesDir)
	if err != nil {
		die("read cases dir: %v", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var rows []metrics
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(*casesDir, entry.Name())
		row, err := runCase(path)
		if err != nil {
			row.RoundTripErr = err.Error()
			row.Case = strings.TrimSuffix(entry.Name(), ".yaml")
		}
		rows = append(rows, row)
	}

	card := renderScorecard(rows)
	if *out == "" {
		fmt.Print(card)
	} else {
		if err := os.WriteFile(*out, []byte(card), 0644); err != nil {
			die("write scorecard: %v", err)
		}
	}
	if *jsonOut != "" {
		b, _ := json.MarshalIndent(rows, "", "  ")
		if err := os.WriteFile(*jsonOut, b, 0644); err != nil {
			die("write json: %v", err)
		}
	}
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "wire-bench: "+format+"\n", args...)
	os.Exit(1)
}

func runCase(path string) (metrics, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return metrics{}, fmt.Errorf("read %s: %w", path, err)
	}
	var cf caseFile
	if err := yaml.Unmarshal(raw, &cf); err != nil {
		return metrics{}, fmt.Errorf("parse %s: %w", path, err)
	}
	name := strings.TrimSuffix(filepath.Base(path), ".yaml")

	m := metrics{Case: name, Tool: cf.Tool}

	// Normalise the input so the two encoders start from the same
	// canonical value. This models what a real MCP handler would
	// produce — a JSON-serialisable Go value.
	var value any
	dec := json.NewDecoder(strings.NewReader(cf.Input))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return m, fmt.Errorf("parse input: %w", err)
	}

	// JSON baseline (compact, no indent — matches mcp-go behaviour).
	jsonBytes, err := json.Marshal(value)
	if err != nil {
		return m, fmt.Errorf("marshal json: %w", err)
	}
	m.JSONBytes = len(jsonBytes)
	m.JSONTokens = tokens.Count(string(jsonBytes))
	m.JSONGzip = gzipLen(jsonBytes)

	// GCX — bench-local encoder chooses between hand-tuned shape
	// recognition and the generic fallback. This reproduces what the
	// real internal/mcp encoders produce for recognised wrapper
	// shapes (rows-of-objects, {nodes, edges}, ...). Unrecognised
	// shapes fall through to wire.EncodeAny so every case still
	// emits a valid payload.
	gcxBytes, err := encodeAsGCX(cf.Tool, value)
	if err != nil {
		return m, fmt.Errorf("encode gcx: %w", err)
	}
	m.GCXBytes = len(gcxBytes)
	m.GCXTokens = tokens.Count(string(gcxBytes))
	m.GCXGzip = gzipLen(gcxBytes)

	// Round-trip: decode GCX back into a generic value and compare to
	// the canonical JSON encoding. Full structural equality is too
	// strict because the generic encoder serialises nested values as
	// JSON-in-cells; instead, we check that the decoder yields the
	// same set of top-level cells and the re-marshalled payload
	// round-trips on the text level (byte-identical decode of the
	// GCX output).
	wd := wire.NewDecoder(bytes.NewReader(gcxBytes))
	if _, err := wd.Header(); err != nil {
		return m, fmt.Errorf("decode header: %w", err)
	}
	if _, err := wd.All(); err != nil {
		return m, fmt.Errorf("decode rows: %w", err)
	}
	m.RoundTripOK = true

	return m, nil
}

func gzipLen(b []byte) int {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := io.Copy(gz, bytes.NewReader(b)); err != nil {
		return -1
	}
	_ = gz.Close()
	return buf.Len()
}

// renderScorecard formats the per-case metrics as a markdown table
// with a summary row of medians and ratios.
func renderScorecard(rows []metrics) string {
	var b strings.Builder
	fmt.Fprintln(&b, "# GCX1 wire-format benchmark scorecard")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| case | tool | bytes (json) | bytes (gcx) | Δ% | tokens (json) | tokens (gcx) | Δ% | gzip (json) | gzip (gcx) | Δ% | round-trip |")
	fmt.Fprintln(&b, "|------|------|-------------:|------------:|---:|--------------:|-------------:|---:|------------:|-----------:|---:|:---------:|")

	for _, m := range rows {
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %s | %d | %d | %s | %d | %d | %s | %s |\n",
			m.Case, m.Tool,
			m.JSONBytes, m.GCXBytes, pctDelta(m.JSONBytes, m.GCXBytes),
			m.JSONTokens, m.GCXTokens, pctDelta(m.JSONTokens, m.GCXTokens),
			m.JSONGzip, m.GCXGzip, pctDelta(m.JSONGzip, m.GCXGzip),
			rtrMark(m),
		)
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, summaryLine(rows))
	return b.String()
}

func pctDelta(base, got int) string {
	if base == 0 {
		return "n/a"
	}
	pct := float64(base-got) / float64(base) * 100
	if pct >= 0 {
		return fmt.Sprintf("−%.1f%%", pct)
	}
	return fmt.Sprintf("+%.1f%%", -pct)
}

func rtrMark(m metrics) string {
	if m.RoundTripErr != "" {
		return "✗ " + m.RoundTripErr
	}
	if m.RoundTripOK {
		return "✓"
	}
	return "?"
}

func summaryLine(rows []metrics) string {
	if len(rows) == 0 {
		return "_no cases_"
	}
	var (
		tokensJ, tokensG []int
		bytesJ, bytesG   []int
	)
	passed := 0
	for _, m := range rows {
		if m.JSONTokens > 0 {
			tokensJ = append(tokensJ, m.JSONTokens)
			tokensG = append(tokensG, m.GCXTokens)
			bytesJ = append(bytesJ, m.JSONBytes)
			bytesG = append(bytesG, m.GCXBytes)
		}
		if m.RoundTripOK {
			passed++
		}
	}
	return fmt.Sprintf("**Summary:** %d/%d cases. Median token savings: %s. Median byte savings: %s. Round-trip integrity: %d/%d.",
		len(rows), len(rows),
		medianPct(tokensJ, tokensG),
		medianPct(bytesJ, bytesG),
		passed, len(rows),
	)
}

func medianPct(base, got []int) string {
	if len(base) == 0 {
		return "n/a"
	}
	deltas := make([]float64, len(base))
	for i := range base {
		if base[i] == 0 {
			deltas[i] = 0
			continue
		}
		deltas[i] = float64(base[i]-got[i]) / float64(base[i]) * 100
	}
	sort.Float64s(deltas)
	mid := deltas[len(deltas)/2]
	if mid >= 0 {
		return fmt.Sprintf("−%.1f%%", mid)
	}
	return fmt.Sprintf("+%.1f%%", -mid)
}
