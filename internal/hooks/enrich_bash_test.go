package hooks

import (
	"strings"
	"testing"
)

// newIndexedBridge stubs the daemon file-indexed probe so every queried
// file looks indexed with the given symbol count, for the duration of the
// test. Used to exercise enrichBash's ReadSource path without a real daemon.
// Returns a dummy port (0) so the legacy `port := newIndexedBridge(...)`
// call sites still compile; the value is unused now that the indexed check
// routes through the stubbed fileIndexedFn seam rather than an HTTP port.
func newIndexedBridge(t *testing.T, symbols int) int {
	t.Helper()
	prev := fileIndexedFn
	t.Cleanup(func() { fileIndexedFn = prev })
	fileIndexedFn = func(_, _ string) (bool, int) { return symbols > 0, symbols }
	return 0
}

func TestEnrichBash_GrepHit_Denies(t *testing.T) {
	redirectTelemetry(t)
	stubProbe(t, []grepSymbolHit{
		{Name: "handleFoo", Kind: "function", FilePath: "internal/a.go", Line: 42},
	}, nil)

	r := enrichBash(map[string]any{"command": `grep -rn "handleFoo" .`}, "")
	if !r.deny {
		t.Fatalf("expected deny on grep hit, got %+v", r)
	}
	if !strings.Contains(r.reason, "handleFoo") {
		t.Error("deny reason should mention the pattern")
	}
	if !strings.Contains(r.reason, "internal/a.go:42") {
		t.Error("deny reason should list the hit")
	}
}

func TestEnrichBash_GrepMiss_SoftGuidance(t *testing.T) {
	redirectTelemetry(t)
	stubProbe(t, nil, nil) // daemon reachable, no hits

	r := enrichBash(map[string]any{"command": `grep -rn "handleFoo" .`}, "")
	if r.deny {
		t.Fatal("miss should not deny")
	}
	if !strings.Contains(r.context, "search_symbols") {
		t.Error("miss should return soft guidance mentioning search_symbols")
	}
}

func TestEnrichBash_GrepPiped_PassesThrough(t *testing.T) {
	// grep after | is a filter on upstream output — not a codebase search.
	rec := stubProbe(t, nil, nil)
	r := enrichBash(map[string]any{"command": `go test ./... | grep FAIL`}, "")
	if r.deny || r.context != "" {
		t.Errorf("piped grep should pass through, got %+v", r)
	}
	if len(rec.calls) != 0 {
		t.Errorf("piped grep should not probe daemon, got calls %v", rec.calls)
	}
}

func TestEnrichBash_RgBare_Denies(t *testing.T) {
	redirectTelemetry(t)
	stubProbe(t, []grepSymbolHit{
		{Name: "MyType", Kind: "type", FilePath: "a.go", Line: 5},
	}, nil)

	r := enrichBash(map[string]any{"command": `rg MyType`}, "")
	if !r.deny {
		t.Fatalf("expected deny, got %+v", r)
	}
}

func TestEnrichBash_FindName_Denies(t *testing.T) {
	redirectTelemetry(t)
	stubProbe(t, []grepSymbolHit{
		{Name: "Handler", Kind: "type", FilePath: "x.go", Line: 10},
	}, nil)

	r := enrichBash(map[string]any{"command": `find . -name "Handler*"`}, "")
	if !r.deny {
		t.Fatalf("expected deny for find -name with symbol-shaped root, got %+v", r)
	}
}

func TestEnrichBash_FindNameGoFiles_NoProbe(t *testing.T) {
	// `-name "*.go"` reduces to ".go" which is not symbol-shaped — no probe,
	// no deny. Returns soft guidance because the pattern is >2 chars.
	rec := stubProbe(t, nil, nil)
	r := enrichBash(map[string]any{"command": `find . -name "*.go"`}, "")
	if r.deny {
		t.Fatal("find -name *.go should not deny")
	}
	if len(rec.calls) != 0 {
		t.Errorf("non-symbol-shaped name should not probe, got %v", rec.calls)
	}
}

func TestEnrichBash_FindTypeD_Passthrough(t *testing.T) {
	rec := stubProbe(t, nil, nil)
	r := enrichBash(map[string]any{"command": `find . -maxdepth 3 -type d`}, "")
	if r.deny || r.context != "" {
		t.Errorf("find -type d should pass through, got %+v", r)
	}
	if len(rec.calls) != 0 {
		t.Error("find without -name should not probe")
	}
}

func TestEnrichBash_CatIndexedSource_Denies(t *testing.T) {
	newIndexedBridge(t, 17)
	r := enrichBash(map[string]any{"command": `cat /repo/handler.go`}, "")
	if !r.deny {
		t.Fatalf("expected deny for cat of indexed source, got %+v", r)
	}
	if !strings.Contains(r.reason, "/repo/handler.go") {
		t.Error("deny reason should mention the file path")
	}
	if !strings.Contains(r.reason, "17 symbols") {
		t.Error("deny reason should include the symbol count")
	}
	if !strings.Contains(r.reason, "get_file_summary") {
		t.Error("deny reason should point to get_file_summary")
	}
}

func TestEnrichBash_CatUnindexedSource_SoftGuidance(t *testing.T) {
	// probe not stubbed → file treated as not indexed.
	r := enrichBash(map[string]any{"command": `head -20 /tmp/foo.go`}, "")
	if r.deny {
		t.Fatal("unindexed source should not deny")
	}
	if !strings.Contains(r.context, "get_symbol_source") {
		t.Error("soft guidance should mention get_symbol_source")
	}
}

func TestEnrichBash_CatLogfile_Passthrough(t *testing.T) {
	r := enrichBash(map[string]any{"command": `cat /tmp/app.log`}, "")
	if r.deny || r.context != "" {
		t.Errorf("cat of non-source file should pass through, got %+v", r)
	}
}

func TestEnrichBash_EmptyCommand(t *testing.T) {
	r := enrichBash(map[string]any{"command": ""}, "")
	if r.deny || r.context != "" {
		t.Errorf("empty command should pass through, got %+v", r)
	}
}

func TestEnrichBash_UnrelatedCommand(t *testing.T) {
	rec := stubProbe(t, nil, nil)
	for _, cmd := range []string{
		`ls /repo`,
		`go build ./...`,
		`git status`,
		`echo hello`,
	} {
		r := enrichBash(map[string]any{"command": cmd}, "")
		if r.deny || r.context != "" {
			t.Errorf("%q should pass through, got %+v", cmd, r)
		}
	}
	if len(rec.calls) != 0 {
		t.Errorf("unrelated commands should not probe, got %v", rec.calls)
	}
}

func TestEnrichBash_TelemetryTaggedAsBash(t *testing.T) {
	logPath := redirectTelemetry(t)
	stubProbe(t, []grepSymbolHit{
		{Name: "handleFoo", Kind: "function", FilePath: "a.go", Line: 1},
	}, nil)

	_ = enrichBash(map[string]any{"command": `grep -rn handleFoo .`}, "")

	recs := readDecisions(t, logPath)
	if len(recs) != 1 {
		t.Fatalf("expected 1 telemetry record, got %d", len(recs))
	}
	if recs[0].Tool != "Bash" {
		t.Errorf("tool = %q, want %q", recs[0].Tool, "Bash")
	}
	if recs[0].Decision != DecisionProbedHit {
		t.Errorf("decision = %v, want probed_hit", recs[0].Decision)
	}
}
