package embedding

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// tinyWordPieceTokenizer is a minimal handcrafted BERT-style WordPiece tokenizer.json
// (lowercasing normalizer, whitespace pre-tokenizer, ~14-entry vocab). Each
// whitespace-separated in-vocab word encodes to exactly one token, which makes the
// truncation cut points deterministic without a real model download.
const tinyWordPieceTokenizer = `{
  "version": "1.0",
  "truncation": null,
  "padding": null,
  "added_tokens": [
    {"id": 0, "content": "[PAD]", "single_word": false, "lstrip": false, "rstrip": false, "normalized": false, "special": true},
    {"id": 100, "content": "[UNK]", "single_word": false, "lstrip": false, "rstrip": false, "normalized": false, "special": true},
    {"id": 101, "content": "[CLS]", "single_word": false, "lstrip": false, "rstrip": false, "normalized": false, "special": true},
    {"id": 102, "content": "[SEP]", "single_word": false, "lstrip": false, "rstrip": false, "normalized": false, "special": true}
  ],
  "normalizer": {"type": "BertNormalizer", "lowercase": true},
  "pre_tokenizer": {"type": "BertPreTokenizer"},
  "post_processor": null,
  "decoder": {"type": "WordPiece", "prefix": "##"},
  "model": {
    "type": "WordPiece",
    "unk_token": "[UNK]",
    "continuing_subword_prefix": "##",
    "max_input_chars_per_word": 100,
    "vocab": {
      "[PAD]": 0,
      "hello": 1,
      "world": 2,
      "test": 3,
      "[UNK]": 100,
      "[CLS]": 101,
      "[SEP]": 102,
      "the": 104,
      "a": 105,
      "is": 106,
      "this": 107
    }
  }
}`

// writeModelDir creates a temp model directory containing tokenizer.json and,
// optionally, a config.json declaring max_position_embeddings.
func writeModelDir(t *testing.T, maxPos int) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tokenizer.json"), []byte(tinyWordPieceTokenizer), 0o644); err != nil {
		t.Fatal(err)
	}
	if maxPos > 0 {
		cfg := fmt.Sprintf(`{"max_position_embeddings": %d}`, maxPos)
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestNewTokenTruncator_BudgetFromConfig(t *testing.T) {
	// max_position_embeddings 7 minus the two reserved special-token slots = budget 5.
	dir := writeModelDir(t, 7)
	tr, err := newTokenTruncator(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tr.tk == nil {
		t.Fatal("expected a real tokenizer to load")
	}
	if tr.budget != 5 {
		t.Fatalf("budget = %d, want 5", tr.budget)
	}
}

func TestNewTokenTruncator_MissingConfigFallback(t *testing.T) {
	dir := writeModelDir(t, 0) // no config.json
	tr, err := newTokenTruncator(dir)
	if err == nil {
		t.Fatal("expected an informational error about the fallback budget")
	}
	if tr.tk == nil {
		t.Fatal("tokenizer must still load even when config.json is missing")
	}
	if tr.budget != fallbackTokenBudget {
		t.Fatalf("budget = %d, want fallback %d", tr.budget, fallbackTokenBudget)
	}
}

func TestTokenTruncator_ShortTextUntouched(t *testing.T) {
	dir := writeModelDir(t, 7) // budget 5
	tr, err := newTokenTruncator(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 5 runes ≤ budget 5 → fast path returns the text verbatim.
	if got := tr.Truncate("hello"); got != "hello" {
		t.Fatalf("Truncate(hello) = %q, want unchanged", got)
	}
	// 11 runes > budget but only 2 tokens ≤ budget → token-count check returns verbatim.
	if got := tr.Truncate("hello world"); got != "hello world" {
		t.Fatalf("Truncate(hello world) = %q, want unchanged", got)
	}
}

func TestTokenTruncator_OverBudgetCutAtSpanBoundary(t *testing.T) {
	dir := writeModelDir(t, 7) // budget 5
	tr, err := newTokenTruncator(dir)
	if err != nil {
		t.Fatal(err)
	}
	const input = "hello world test the a is this" // 7 in-vocab words → 7 tokens
	if n := len(tr.tk.EncodeWithAnnotations(input).IDs); n <= tr.budget {
		t.Fatalf("test precondition: input encodes to %d tokens, need > budget %d", n, tr.budget)
	}
	got := tr.Truncate(input)
	if got != "hello world test the a" {
		t.Fatalf("Truncate cut = %q, want %q", got, "hello world test the a")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncated text is not valid UTF-8: %q", got)
	}
	if n := len(tr.tk.EncodeWithAnnotations(got).IDs); n > tr.budget {
		t.Fatalf("truncated text still encodes to %d tokens, over budget %d", n, tr.budget)
	}
}

// TestTokenTruncator_TruncateAll covers the batch API actually wired into every
// provider's EmbedBatch: over-budget entries are shortened, in-budget entries are
// returned untouched, and an all-fits batch returns the input slice unmodified.
func TestTokenTruncator_TruncateAll(t *testing.T) {
	dir := writeModelDir(t, 7) // budget 5
	tr, err := newTokenTruncator(dir)
	if err != nil {
		t.Fatal(err)
	}
	const over = "hello world test the a is this" // 7 tokens > budget 5

	out := tr.TruncateAll([]string{over, "hello", "hello world"})
	if len(out) != 3 {
		t.Fatalf("got %d results, want 3", len(out))
	}
	if len(tr.tk.EncodeWithAnnotations(out[0]).IDs) > tr.budget {
		t.Fatalf("over-budget entry not truncated: %q", out[0])
	}
	if out[1] != "hello" || out[2] != "hello world" {
		t.Fatalf("in-budget entries changed: %q, %q", out[1], out[2])
	}

	// Nothing over budget -> the exact same slice is returned (no allocation).
	fits := []string{"hello", "world"}
	if got := tr.TruncateAll(fits); &got[0] != &fits[0] {
		t.Fatal("TruncateAll should return the input slice when nothing needs truncating")
	}
}

func TestReadTokenBudget(t *testing.T) {
	cases := []struct {
		name       string
		configBody string // "" means no config.json
		want       int
		wantErr    bool
	}{
		{"bert-512", `{"max_position_embeddings": 512}`, 510, false},
		{"jina-8192", `{"max_position_embeddings": 8192}`, 8190, false},
		{"missing", "", fallbackTokenBudget, true},
		{"unparseable", `{not json`, fallbackTokenBudget, true},
		{"absent-field", `{"hidden_size": 384}`, fallbackTokenBudget, true},
		{"implausible", `{"max_position_embeddings": 1}`, fallbackTokenBudget, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.configBody != "" {
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(tc.configBody), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := readTokenBudget(dir)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("budget = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestClampRunes(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		maxRunes int
		want     string
	}{
		{"multibyte not split", "héllo wörld", 3, "hél"},
		{"under cap untouched", "abc", 10, "abc"},
		{"zero cap", "abc", 0, ""},
		{"exact cap", "abcd", 4, "abcd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clampRunes(tc.text, tc.maxRunes)
			if got != tc.want {
				t.Fatalf("clampRunes(%q, %d) = %q, want %q", tc.text, tc.maxRunes, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("result not valid UTF-8: %q", got)
			}
		})
	}
}

// TestHugotProvider_LiveTruncatesOverBudgetInput is the end-to-end regression
// for the shape-mismatch crash: an input well past MiniLM's 512-token window
// used to abort the pipeline. With truncation it must embed to a real 384-dim
// vector. Gated on a real cached model — set GORTEX_TEST_LIVE_MODELS=1.
func TestHugotProvider_LiveTruncatesOverBudgetInput(t *testing.T) {
	if os.Getenv("GORTEX_TEST_LIVE_MODELS") != "1" {
		t.Skip("set GORTEX_TEST_LIVE_MODELS=1 to run against the real MiniLM model")
	}
	prov, err := newHugotProvider()
	if err != nil {
		t.Skipf("MiniLM model unavailable: %v", err)
	}
	defer prov.Close()

	// ~120 repetitions of a 9-token sentence ≈ 1000+ tokens — comfortably past
	// the 512-token window that used to crash the GO tokenizer path.
	over := strings.Repeat("the quick brown fox jumps over the lazy dog ", 120)

	vec, err := prov.Embed(context.Background(), over)
	if err != nil {
		t.Fatalf("embedding an over-budget input failed (truncation regression): %v", err)
	}
	if len(vec) != prov.Dimensions() {
		t.Fatalf("got a %d-dim vector, want %d", len(vec), prov.Dimensions())
	}
	nonZero := false
	for _, v := range vec {
		if v != 0 {
			nonZero = true
			break
		}
	}
	if !nonZero {
		t.Fatal("embedding vector is all zeros")
	}

	// A batch mixing an over-budget input with a short one must also succeed.
	vecs, err := prov.EmbedBatch(context.Background(), []string{over, "short input"})
	if err != nil {
		t.Fatalf("mixed batch failed: %v", err)
	}
	if len(vecs) != 2 || len(vecs[0]) != prov.Dimensions() || len(vecs[1]) != prov.Dimensions() {
		t.Fatalf("mixed batch produced wrong shapes: %d vectors", len(vecs))
	}
}

func TestNewTokenTruncator_CorruptTokenizerRuneClamp(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tokenizer.json"), []byte("{ not a tokenizer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"max_position_embeddings": 12}`), 0o644); err != nil {
		t.Fatal(err)
	}
	tr, err := newTokenTruncator(dir)
	if err == nil {
		t.Fatal("expected an error when tokenizer.json is corrupt")
	}
	if tr == nil {
		t.Fatal("truncator must be non-nil even on tokenizer failure")
	}
	if tr.tk != nil {
		t.Fatal("tk must be nil in rune-clamp mode")
	}
	// clamp must equal the budget (not a multiple): a token spans >= 1 rune, so
	// budget runes bound the token count to budget — a looser cap would not.
	if tr.budget != 10 || tr.clamp != 10 {
		t.Fatalf("budget=%d clamp=%d, want 10/10", tr.budget, tr.clamp)
	}
	// A long multibyte text is clamped (not split mid-rune) rather than passed through.
	long := ""
	for i := 0; i < 100; i++ {
		long += "café "
	}
	got := tr.Truncate(long)
	if utf8.RuneCountInString(got) > tr.clamp {
		t.Fatalf("clamped rune count %d exceeds clamp %d", utf8.RuneCountInString(got), tr.clamp)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("clamped text not valid UTF-8")
	}
}
