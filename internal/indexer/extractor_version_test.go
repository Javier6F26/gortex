package indexer

import (
	"reflect"
	"testing"
)

// TestStaleLangsDetection proves the per-language extractor-staleness signal:
// only languages whose stored version is behind the current one are flagged
// (so the advisory names the exact languages to reindex — a scoped reindex
// rather than a full cold rebuild), a language the snapshot never recorded is
// never spuriously flagged, and an empty baseline reports nothing.
func TestStaleLangsDetection(t *testing.T) {
	t.Run("only_behind_langs", func(t *testing.T) {
		stored := map[string]int{"go": 1, "python": 2, "ruby": 1}
		current := map[string]int{"go": 2, "python": 2, "ruby": 1, "rust": 3}
		got := staleLangsBetween(stored, current)
		// go is behind (1<2); python/ruby are current; rust is absent from
		// stored (no baseline) so it is NOT flagged.
		if want := []string{"go"}; !reflect.DeepEqual(got, want) {
			t.Errorf("staleLangsBetween = %v, want %v", got, want)
		}
	})

	t.Run("sorted_multiple", func(t *testing.T) {
		stored := map[string]int{"typescript": 1, "go": 1, "python": 1}
		current := map[string]int{"typescript": 2, "go": 2, "python": 1}
		got := staleLangsBetween(stored, current)
		if want := []string{"go", "typescript"}; !reflect.DeepEqual(got, want) {
			t.Errorf("staleLangsBetween = %v, want %v (sorted)", got, want)
		}
	})

	t.Run("json_and_empty", func(t *testing.T) {
		// An empty / unparseable baseline reports nothing.
		if got := ExtractorVersionStaleLangs(""); got != nil {
			t.Errorf("empty baseline = %v, want nil", got)
		}
		if got := ExtractorVersionStaleLangs("not json"); got != nil {
			t.Errorf("bad json = %v, want nil", got)
		}
		// Against the live extractor versions (all baseline 1), a stored go@1
		// is not stale.
		if got := ExtractorVersionStaleLangs(`{"go":1}`); len(got) != 0 {
			t.Errorf("stored at current = %v, want empty", got)
		}
	})

	t.Run("lang_for_file", func(t *testing.T) {
		if got := ExtractorLangForFile("internal/auth/token.go"); got != "go" {
			t.Errorf("ExtractorLangForFile(.go) = %q, want go", got)
		}
		if got := ExtractorLangForFile("README.zzz"); got != "" {
			t.Errorf("ExtractorLangForFile(unknown) = %q, want \"\"", got)
		}
	})
}

// TestMarkdownExtractorReextraction proves the underscore-preservation fix is
// deployable: markdown / quarto are registered in the extractor-version salt
// registry at version 2, so already-indexed docs re-extract on the next
// reconcile even though their content is unchanged. Without the salt, a
// deployed store would keep the mangled (underscore-stripped) section_text
// until the file content itself changed.
func TestMarkdownExtractorReextraction(t *testing.T) {
	// A prose file now carries a non-empty Merkle salt — the leaf differs
	// from the pre-fix (no-salt) leaf, so the reconcile flags it stale.
	for _, f := range []string{"docs/notes.md", "README.markdown", "report.qmd"} {
		if got := merkleSaltFor(f); got == "" {
			t.Errorf("merkleSaltFor(%q) = %q, want non-empty salt so the file re-extracts", f, got)
		}
	}
	if got := ExtractorLangForFile("constitution.md"); got != "markdown" {
		t.Errorf("ExtractorLangForFile(.md) = %q, want markdown", got)
	}

	// A store last indexed by a binary that recorded markdown@1 (or the
	// baseline) is flagged stale against the current markdown@2 — the
	// freshness rider names markdown for a scoped reindex.
	if got := ExtractorVersionStaleLangs(`{"markdown":1,"go":1}`); !reflect.DeepEqual(got, []string{"markdown"}) {
		t.Errorf("stale langs = %v, want [markdown]", got)
	}
}
