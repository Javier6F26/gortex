package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveRepoSpec_PresetSlug(t *testing.T) {
	s, err := resolveRepoSpec("gin")
	if err != nil {
		t.Fatalf("resolveRepoSpec(gin): %v", err)
	}
	if s.Slug != "gin" || !strings.Contains(s.URL, "gin-gonic/gin") {
		t.Errorf("gin preset = %+v, want slug=gin URL containing gin-gonic/gin", s)
	}
}

func TestResolveRepoSpec_OwnerRepoShorthand(t *testing.T) {
	s, err := resolveRepoSpec("foo/bar")
	if err != nil {
		t.Fatalf("resolveRepoSpec(foo/bar): %v", err)
	}
	if s.Slug != "bar" || s.URL != "https://github.com/foo/bar.git" {
		t.Errorf("owner/repo shorthand = %+v, want github URL", s)
	}
}

func TestResolveRepoSpec_HTTPSPath(t *testing.T) {
	s, err := resolveRepoSpec("https://example.com/path/to/myrepo.git")
	if err != nil {
		t.Fatalf("resolveRepoSpec(URL): %v", err)
	}
	if s.URL != "https://example.com/path/to/myrepo.git" || s.Slug != "myrepo" {
		t.Errorf("URL spec = %+v, want slug=myrepo", s)
	}
}

func TestResolveRepoSpec_LocalPath(t *testing.T) {
	dir := t.TempDir()
	s, err := resolveRepoSpec("local:" + dir)
	if err != nil {
		t.Fatalf("resolveRepoSpec(local:): %v", err)
	}
	if !s.Local || s.Path != dir {
		t.Errorf("local spec = %+v, want Local=true Path=%s", s, dir)
	}
}

func TestResolveRepoSpec_Empty(t *testing.T) {
	if _, err := resolveRepoSpec(""); err == nil {
		t.Error("expected error for empty repo token")
	}
}

func TestResolveRepoSpec_UnknownSlug(t *testing.T) {
	if _, err := resolveRepoSpec("nonexistent"); err == nil {
		t.Error("expected error for unknown bare slug")
	}
}

func TestLoadQueries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queries.json")
	body := `{"queries": ["FooBar", "validate_token", "auth middleware"]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadQueries(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "FooBar" {
		t.Errorf("got %+v, want 3 queries starting with FooBar", got)
	}
}

func TestLoadQueries_MissingFile(t *testing.T) {
	if _, err := loadQueries(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoadQueries_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadQueries(path); err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestPctMs(t *testing.T) {
	cases := []struct {
		name string
		in   []time.Duration
		pct  int
		want float64
	}{
		{"empty", nil, 50, 0},
		{"single", []time.Duration{2 * time.Millisecond}, 50, 2.0},
		{"p50 of 5", []time.Duration{
			1 * time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond,
			4 * time.Millisecond, 5 * time.Millisecond,
		}, 50, 3.0},
		// Nearest-rank percentile: idx = (pct × n) / 100. For
		// durations(20)=[1..20]ms, p95 = sorted[19] = 20ms.
		{"p95 of 20", durations(20), 95, 20.0},
		{"p99 of 100", durations(100), 99, 100.0},
	}
	for _, c := range cases {
		got := pctMs(c.in, c.pct)
		if got != c.want {
			t.Errorf("%s: pctMs = %.2f, want %.2f", c.name, got, c.want)
		}
	}
}

func durations(n int) []time.Duration {
	out := make([]time.Duration, n)
	for i := range out {
		out[i] = time.Duration(i+1) * time.Millisecond
	}
	return out
}

func TestRenderMarkdown_PopulatesTable(t *testing.T) {
	rows := []repoRow{
		{Slug: "gin", LoC: 1000, Files: 50, Nodes: 800, Edges: 1200,
			ColdIndexMs: 250.0, SearchP95Ms: 0.5, ImpactP95Ms: 0.3, ImpactP99Ms: 0.7,
			IncrementalMs: 30, DBBytes: 250_000},
		{Slug: "nestjs", LoC: 5000, Files: 200, Nodes: 4500, Edges: 7000,
			ColdIndexMs: 1200, SearchP95Ms: 2.0, ImpactP95Ms: 1.5, ImpactP99Ms: 3.1,
			IncrementalMs: 60, DBBytes: 1_500_000, BudgetViolations: 1},
		{Slug: "broken", Error: "clone failed"},
	}
	md := renderMarkdown(rows)
	for _, want := range []string{
		"# Reference-repo perf benchmark",
		"| repo | LoC |",
		"| gin |",
		"| nestjs |",
		"⚠ 1",
		"✗ (clone failed)",
		"**Summary:**",
		"2/3 repos succeeded",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("rendered markdown missing %q\n----\n%s", want, md)
		}
	}
}

func TestRenderCSV_HasHeaderAndRows(t *testing.T) {
	rows := []repoRow{
		{Slug: "gin", LoC: 1000, Files: 50, ColdIndexMs: 100, SearchP95Ms: 0.5, ImpactP95Ms: 0.2},
	}
	csvOut := renderCSV(rows)
	if !strings.HasPrefix(csvOut, "slug,path,loc,files,nodes,edges,") {
		t.Errorf("CSV header missing or wrong:\n%s", csvOut)
	}
	if !strings.Contains(csvOut, "gin,") {
		t.Errorf("CSV body missing gin row:\n%s", csvOut)
	}
}

func TestMustMarshalJSON_RoundTrip(t *testing.T) {
	rows := []repoRow{{Slug: "gin", LoC: 100, Files: 5, ImpactP95Ms: 0.5}}
	raw := mustMarshalJSON(rows)
	var got []repoRow
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("round-trip failed: %v", err)
	}
	if len(got) != 1 || got[0].Slug != "gin" || got[0].ImpactP95Ms != 0.5 {
		t.Errorf("round-trip lost data: %+v", got)
	}
}

func TestFmtMs_Buckets(t *testing.T) {
	cases := map[float64]string{
		0:       "—",
		0.25:    "0.25ms",
		3.7:     "3.7ms",
		1500:    "1.50s",
		60_000:  "60.00s",
	}
	for in, want := range cases {
		if got := fmtMs(in); got != want {
			t.Errorf("fmtMs(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestFmtBytes_Buckets(t *testing.T) {
	cases := map[int64]string{
		0:                "—",
		-1:               "—",
		512:              "512B",
		2048:             "2.0KB",
		3 * 1024 * 1024:  "3.0MB",
		2 * 1024 * 1024 * 1024: "2.00GB",
	}
	for in, want := range cases {
		if got := fmtBytes(in); got != want {
			t.Errorf("fmtBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestBudgetMark(t *testing.T) {
	cases := []struct {
		r    repoRow
		want string
	}{
		{repoRow{Error: "boom"}, "✗"},
		{repoRow{BudgetViolations: 0}, "✓"},
		{repoRow{BudgetViolations: 2}, "⚠ 2"},
	}
	for _, c := range cases {
		if got := budgetMark(c.r); got != c.want {
			t.Errorf("budgetMark(%+v) = %q, want %q", c.r, got, c.want)
		}
	}
}

func TestHumanInt(t *testing.T) {
	cases := map[int64]string{
		0:       "0",
		999:     "999",
		1000:    "1,000",
		1234567: "1,234,567",
		-1234:   "-1,234",
	}
	for in, want := range cases {
		if got := humanInt(in); got != want {
			t.Errorf("humanInt(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveCacheDir_PrecedenceOrder(t *testing.T) {
	// Flag wins over env wins over default.
	t.Setenv("GORTEX_BENCH_CACHE", "/from-env")
	if got := resolveCacheDir("/from-flag"); got != "/from-flag" {
		t.Errorf("flag should win: got %q", got)
	}
	if got := resolveCacheDir(""); got != "/from-env" {
		t.Errorf("env should win when flag empty: got %q", got)
	}
	t.Setenv("GORTEX_BENCH_CACHE", "")
	got := resolveCacheDir("")
	if got == "" {
		t.Error("default should not be empty")
	}
}
