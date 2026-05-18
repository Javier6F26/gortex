package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMedianSavedTokens_CL100k(t *testing.T) {
	rows := []tokensMetric{
		{Case: "a", JSONTokens: 100, GCXTokens: 80},   // saved 20
		{Case: "b", JSONTokens: 200, GCXTokens: 150},  // saved 50
		{Case: "c", JSONTokens: 300, GCXTokens: 270},  // saved 30
	}
	if got := medianSavedTokens(rows, false); got != 30 {
		t.Errorf("median = %d, want 30 (sorted [20,30,50] → middle)", got)
	}
}

func TestMedianSavedTokens_Opus47(t *testing.T) {
	rows := []tokensMetric{
		{Case: "a", JSONTokensOpus47: 135, GCXTokensOpus47: 108}, // saved 27
		{Case: "b", JSONTokensOpus47: 270, GCXTokensOpus47: 200}, // saved 70
	}
	if got := medianSavedTokens(rows, true); got != 70 {
		t.Errorf("opus47 median = %d, want 70", got)
	}
}

func TestMedianSavedTokens_ZeroOrNegativeIgnored(t *testing.T) {
	rows := []tokensMetric{
		{Case: "neg", JSONTokens: 100, GCXTokens: 110}, // GCX larger (negative savings) — ignored
		{Case: "zero", JSONTokens: 0, GCXTokens: 0},    // empty — ignored
		{Case: "real", JSONTokens: 100, GCXTokens: 50}, // saved 50
	}
	if got := medianSavedTokens(rows, false); got != 50 {
		t.Errorf("median = %d, want 50 (negative + zero excluded)", got)
	}
}

func TestMedianSavedTokens_EmptyReturnsZero(t *testing.T) {
	if got := medianSavedTokens(nil, false); got != 0 {
		t.Errorf("nil input = %d, want 0", got)
	}
	if got := medianSavedTokens([]tokensMetric{}, false); got != 0 {
		t.Errorf("empty input = %d, want 0", got)
	}
}

func TestRenderUSDCard_PopulatesAllModels(t *testing.T) {
	rows := []tokensMetric{
		{Case: "a", JSONTokens: 100, GCXTokens: 80, JSONTokensOpus47: 135, GCXTokensOpus47: 108},
	}
	card := renderUSDCard(rows, 1000)
	for _, want := range []string{
		"USD savings projection",
		"claude-opus-4",
		"claude-sonnet-4",
		"claude-haiku-4.5",
		"gpt-4o",
		"gpt-4o-mini",
		"$/M input",
		"$/day",
		"$/month",
	} {
		if !strings.Contains(card, want) {
			t.Errorf("USD card missing %q\n----\n%s\n----", want, card)
		}
	}
}

func TestRenderUSDCard_ScalingByResponsesPerDay(t *testing.T) {
	rows := []tokensMetric{
		{Case: "a", JSONTokens: 1_000_000, GCXTokens: 0}, // saved 1M tokens (cleanly scaled)
	}
	c1 := renderUSDCard(rows, 1)
	c1000 := renderUSDCard(rows, 1000)
	// At 1M tokens/response × $15/M input, per-response = $15.00.
	if !strings.Contains(c1, "$15.00") {
		t.Errorf("1 resp/day USD card missing $15.00:\n%s", c1)
	}
	// At 1000 resp/day, daily ≈ $15,000 — confirm 4-figure dollar magnitude.
	if !strings.Contains(c1000, "$15000.00") {
		t.Errorf("1000 resp/day USD card missing $15000.00:\n%s", c1000)
	}
}

func TestRenderUSDCard_OpusUsesOpus47TokensWhenAvailable(t *testing.T) {
	// cl100k=100 tokens, opus47=1_000_000. For claude-opus-4, the
	// card must use the opus47 figure (so the row shows $15.00 at
	// 1 resp/day, not the cl100k $0.0015).
	rows := []tokensMetric{
		{Case: "a", JSONTokens: 100, GCXTokens: 0, JSONTokensOpus47: 1_000_000, GCXTokensOpus47: 0},
	}
	card := renderUSDCard(rows, 1)
	if !strings.Contains(card, "$15.00") {
		t.Errorf("opus row should use opus47 tokens (=$15 at 1M saved); got:\n%s", card)
	}
}

func TestBuildUSDCardJSON_Shape(t *testing.T) {
	rows := []tokensMetric{
		{Case: "a", JSONTokens: 100, GCXTokens: 80},
	}
	out := buildUSDCardJSON(rows, 100)
	if out["median_saved_tokens_cl100k"] != 20 {
		t.Errorf("median field = %v, want 20", out["median_saved_tokens_cl100k"])
	}
	if out["responses_per_day"] != 100 {
		t.Errorf("responses_per_day = %v, want 100", out["responses_per_day"])
	}
	models, ok := out["models"].([]map[string]any)
	if !ok {
		t.Fatalf("models field has wrong type %T", out["models"])
	}
	if len(models) == 0 {
		t.Fatal("expected at least one model in USD JSON")
	}
	for _, m := range models {
		for _, key := range []string{"model", "usd_per_m", "usd_per_resp", "usd_per_day", "usd_per_month"} {
			if _, ok := m[key]; !ok {
				t.Errorf("model row missing %q: %+v", key, m)
			}
		}
	}
}

func TestLoadTokensMetrics_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.json")
	rows := []tokensMetric{
		{Case: "a", Tool: "search_symbols", JSONTokens: 100, GCXTokens: 80},
		{Case: "b", Tool: "find_usages", JSONTokens: 200, GCXTokens: 150, JSONTokensOpus47: 270, GCXTokensOpus47: 200},
	}
	raw, _ := json.MarshalIndent(rows, "", "  ")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadTokensMetrics(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("loaded %d rows, want 2", len(got))
	}
	if got[1].JSONTokensOpus47 != 270 {
		t.Errorf("lost opus47 column: %+v", got[1])
	}
}

func TestOutputPathFor_NoOutDirReturnsEmpty(t *testing.T) {
	old := benchOutDir
	benchOutDir = ""
	defer func() { benchOutDir = old }()
	got, err := outputPathFor("tokens", "markdown")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("no out-dir should return empty, got %q", got)
	}
}

func TestOutputPathFor_BuildsExtensionByFormat(t *testing.T) {
	dir := t.TempDir()
	old := benchOutDir
	benchOutDir = dir
	defer func() { benchOutDir = old }()

	md, err := outputPathFor("tokens", "markdown")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(md) != ".md" {
		t.Errorf("markdown path %q must end .md", md)
	}
	js, err := outputPathFor("tokens", "json")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(js) != ".json" {
		t.Errorf("json path %q must end .json", js)
	}
}

func TestBenchCmd_Registered(t *testing.T) {
	// Smoke test: every subcommand is wired into the bench parent.
	subs := map[string]bool{}
	for _, sub := range benchCmd.Commands() {
		subs[sub.Name()] = true
	}
	for _, want := range []string{"recall", "tokens", "embedders", "swebench", "all"} {
		if !subs[want] {
			t.Errorf("bench parent missing subcommand %q (have %v)", want, subs)
		}
	}
}

func TestSwebenchAvailable_DefaultsHonest(t *testing.T) {
	// On a developer box with python3 + an eval/ directory this
	// returns true; on a stripped-down CI it returns false. Either
	// way, the function must NOT panic and the bench subcommand
	// will respect the result by skipping gracefully.
	_ = swebenchAvailable()
}
