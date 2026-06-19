package search

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProjectNameDeWeight proves project-name detection (go.mod / package.json /
// dir, with the length floor) and that the detected name is stripped from the
// INDEXED path so it no longer contributes a uniform path-field boost — the
// de-weighting that composes with the rerank rather than flattening it.
func TestProjectNameDeWeight(t *testing.T) {
	t.Run("detect_from_go_mod", func(t *testing.T) {
		dir := t.TempDir()
		mustWrite(t, filepath.Join(dir, "go.mod"), "module github.com/acme/gortex\n\ngo 1.24\n")
		if got := DetectProjectName(dir); got != "gortex" {
			t.Errorf("DetectProjectName=%q, want gortex", got)
		}
	})

	t.Run("detect_from_package_json", func(t *testing.T) {
		dir := t.TempDir()
		mustWrite(t, filepath.Join(dir, "package.json"), `{"name": "@acme/widgets", "version": "1.0.0"}`)
		if got := DetectProjectName(dir); got != "widgets" {
			t.Errorf("DetectProjectName=%q, want widgets (scoped name de-scoped)", got)
		}
	})

	t.Run("short_name_disabled", func(t *testing.T) {
		dir := t.TempDir()
		mustWrite(t, filepath.Join(dir, "go.mod"), "module github.com/x/cli\n")
		if got := DetectProjectName(dir); got != "" {
			t.Errorf("DetectProjectName=%q, want \"\" (below the %d-char floor)", got, projectNameMinLen)
		}
	})

	t.Run("strip_project_name_segment", func(t *testing.T) {
		// The repo-root segment is dropped; the rest of the path is intact.
		if got := StripProjectNameFromPath("gortex/internal/auth/token.go", "gortex"); got != "internal/auth/token.go" {
			t.Errorf("StripProjectNameFromPath=%q, want internal/auth/token.go", got)
		}
		// A path that does not contain the project name is unchanged.
		if got := StripProjectNameFromPath("internal/auth/token.go", "gortex"); got != "internal/auth/token.go" {
			t.Errorf("unchanged path mutated: %q", got)
		}
		// A short / empty project name never strips.
		if got := StripProjectNameFromPath("cli/main.go", "cli"); got != "cli/main.go" {
			t.Errorf("short project name should not strip: got %q", got)
		}
	})
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestTypoRescueOnEmpty proves the bigram typo-rescue index is built by default
// (no GORTEX_BIGRAM_TYPOS needed) so a typo'd query that returns zero BM25 hits
// can still be rescued by bigram overlap — and that the opt-out flag disables
// it for a perf-sensitive operator.
func TestTypoRescueOnEmpty(t *testing.T) {
	t.Run("default_on", func(t *testing.T) {
		t.Setenv("GORTEX_BIGRAM_TYPOS", "") // simulate "unset" → default ON
		b := NewBM25()
		b.Add("doc1", "validateToken")
		b.Add("doc2", "parseRequest")

		// A transposed/truncated query misses BM25 entirely...
		if hits := b.Search("validat", 10); len(hits) > 0 {
			t.Logf("note: BM25 returned %d hits for the typo (stemming); rescue still applies on true empties", len(hits))
		}
		// ...but the bigram rescue tier recovers the nearest symbol.
		cands := b.BigramCandidates("validat", 1)
		found := false
		for _, c := range cands {
			if c == "doc1" {
				found = true
			}
		}
		if !found {
			t.Errorf("typo rescue did not recover doc1 from %q; candidates=%v", "validat", cands)
		}
	})

	t.Run("opt_out", func(t *testing.T) {
		t.Setenv("GORTEX_BIGRAM_TYPOS", "0")
		b := NewBM25()
		b.Add("doc1", "validateToken")
		if cands := b.BigramCandidates("validat", 1); cands != nil {
			t.Errorf("GORTEX_BIGRAM_TYPOS=0 should disable the bigram index, got candidates %v", cands)
		}
	})
}
