package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadQueries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queries.json")
	body := `{"queries": ["alpha", "BetaSymbol", "find token"]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadQueries(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[1] != "BetaSymbol" {
		t.Errorf("got %v, want 3 queries containing BetaSymbol", got)
	}
}

func TestLoadGroundTruth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gt.json")
	body := `{"queries": {"alpha": ["a.go", "b.go"], "beta": ["c.go"]}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadGroundTruth(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got["alpha"]) != 2 || got["alpha"][0] != "a.go" {
		t.Errorf("alpha truth = %v, want [a.go, b.go]", got["alpha"])
	}
	if len(got["beta"]) != 1 {
		t.Errorf("beta truth = %v, want 1 path", got["beta"])
	}
}

func TestLoadGroundTruth_MissingReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gt.json")
	body := `{}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadGroundTruth(path)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("missing queries map should yield empty map, got %v", got)
	}
}

func TestRecallAtBudget(t *testing.T) {
	// Returned [a, b, c] with per-file tokens [500, 800, 1000]
	// vs expected [a, c]:
	//
	//   budget 2000 → a (500), b (1300), c (2300 > 2000 → stop)
	//                  → hits a only (c excluded) → 1/2 = 0.5
	//   budget 5000 → all included → hits a + c → 2/2 = 1.0
	//   budget 0    → unbounded, all included → 1.0
	r := pipelineResult{
		Returned:      []string{"a", "b", "c"},
		PerFileTokens: []int{500, 800, 1000},
	}
	expected := []string{"a", "c"}
	if got := recallAtBudget(r, expected, 2000); got != 0.5 {
		t.Errorf("recall@2000 = %.2f, want 0.5", got)
	}
	if got := recallAtBudget(r, expected, 5000); got != 1.0 {
		t.Errorf("recall@5000 = %.2f, want 1.0", got)
	}
	if got := recallAtBudget(r, expected, 0); got != 1.0 {
		t.Errorf("recall@0 (unbounded) = %.2f, want 1.0", got)
	}
}

func TestRecallAtBudget_EmptyExpectedReturnsZero(t *testing.T) {
	r := pipelineResult{Returned: []string{"x"}, PerFileTokens: []int{10}}
	if got := recallAtBudget(r, nil, 1000); got != 0 {
		t.Errorf("empty expected = %.2f, want 0 (no recall to measure)", got)
	}
}

func TestMedianTokensFn(t *testing.T) {
	rows := []benchRow{
		{Gortex: pipelineResult{Tokens: 30}},
		{Gortex: pipelineResult{Tokens: 10}},
		{Gortex: pipelineResult{Tokens: 20}},
	}
	got := medianTokensFn(rows, func(r benchRow) int { return r.Gortex.Tokens })
	if got != 20 {
		t.Errorf("median = %d, want 20", got)
	}
}

func TestMedianFloatFn(t *testing.T) {
	rows := []benchRow{
		{RecallAt2kGortex: 0.5},
		{RecallAt2kGortex: 1.0},
		{RecallAt2kGortex: 0.25},
	}
	got := medianFloatFn(rows, func(r benchRow) float64 { return r.RecallAt2kGortex })
	if got != 0.5 {
		t.Errorf("median = %.2f, want 0.5", got)
	}
}

func TestRenderMarkdown_PopulatesAllColumns(t *testing.T) {
	rows := []benchRow{
		{Query: "AddObservation",
			Expected:    []string{"internal/savings/store.go"},
			RipgrepFull: pipelineResult{Tokens: 31530, Returned: []string{"internal/savings/store.go"}, PerFileTokens: []int{31530}},
			RipgrepCtx:  pipelineResult{Tokens: 9020, Returned: []string{"internal/savings/store.go"}, PerFileTokens: []int{9020}},
			Gortex:      pipelineResult{Tokens: 972, Returned: []string{"internal/savings/store.go"}, PerFileTokens: []int{972}},
			RecallAt2kGortex: 1.0,
		},
	}
	md := renderMarkdown(rows, true)
	for _, want := range []string{
		"# Token-efficiency benchmark",
		"AddObservation",
		"31530",
		"9020",
		"972",
		"1.00",
		"**Summary:**",
		"Median tokens",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("rendered markdown missing %q\n----\n%s", want, md)
		}
	}
}

func TestRenderMarkdown_GortexOnlyMode(t *testing.T) {
	rows := []benchRow{
		{Query: "q",
			Gortex:           pipelineResult{Tokens: 100},
			RecallAt2kGortex: 0.75,
		},
	}
	md := renderMarkdown(rows, false)
	if !strings.Contains(md, "gortex only — rg not installed") {
		t.Errorf("gortex-only summary missing the rg-unavailable note:\n%s", md)
	}
	if !strings.Contains(md, "—") {
		t.Errorf("rg columns should render as — when rg not available:\n%s", md)
	}
}

func TestMustMarshalJSON_RoundTrip(t *testing.T) {
	rows := []benchRow{
		{Query: "q", Gortex: pipelineResult{Tokens: 100, Returned: []string{"a.go"}}, RecallAt2kGortex: 0.5},
	}
	raw := mustMarshalJSON(rows)
	var got []benchRow
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("round-trip: %v", err)
	}
	if len(got) != 1 || got[0].Query != "q" || got[0].RecallAt2kGortex != 0.5 {
		t.Errorf("round-trip lost data: %+v", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("short string unchanged: got %q", got)
	}
	if got := truncate("this is a very long query string", 10); got != "this is a…" {
		t.Errorf("long string truncated: got %q", got)
	}
}
