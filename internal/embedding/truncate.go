package embedding

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/gomlx/go-huggingface/tokenizers/api"
	"github.com/gomlx/go-huggingface/tokenizers/hftokenizer"
)

const (
	// fallbackTokenBudget is the token budget used when a model directory has no
	// parseable config.json to read max_position_embeddings from. 512 - 2 = 510,
	// matching the classic BERT/MiniLM context window minus the two special tokens.
	fallbackTokenBudget = 510

	// specialTokenReserve is the number of positions the inference pipeline consumes
	// for special tokens ([CLS]/[SEP]) appended after client-side truncation, so the
	// truncation budget is max_position_embeddings minus this.
	specialTokenReserve = 2

	// runeClampBudgetFactor caps the rune-clamp fallback at budget runes. A
	// WordPiece token spans at least one rune, so token_count <= rune_count;
	// clamping to budget runes therefore guarantees token_count <= budget. A
	// looser cap (e.g. 4*budget) would NOT bound the token count and could let a
	// token-dense input (CJK, single-char words) overflow the window it is meant
	// to protect. Recall loss in this rare fallback is an acceptable price for a
	// hard safety bound.
	runeClampBudgetFactor = 1
)

// tokenTruncator caps input texts at a model's positional budget before they reach
// the inference pipeline.
//
// The pure-Go Hugot tokenizer path does not honour max_position_embeddings, so a
// text longer than the model's context window reaches inference at full length and
// produces a tensor shape mismatch that aborts the entire vector index. Truncating
// client-side keeps that failure from ever occurring; because a transformer cannot
// attend past its positional budget, cutting the tail is lossless by construction.
//
// A tokenTruncator returned by newTokenTruncator is always safe to use: if the real
// tokenizer could not be loaded, tk is nil and Truncate degrades to a rune clamp
// rather than disabling truncation (and the caller's local backend) entirely.
type tokenTruncator struct {
	tk     *hftokenizer.Tokenizer // nil ⇒ rune-clamp fallback
	budget int                    // max token count before truncation (max_position_embeddings - 2)
	clamp  int                    // rune cap used when tk == nil
}

// newTokenTruncator builds a truncator for the model cached under modelDir. It reads
// <modelDir>/tokenizer.json (the same file hugot loads) and derives the token budget
// from <modelDir>/config.json's max_position_embeddings.
//
// The returned truncator is always non-nil and usable. A non-nil error is
// informational: it reports a degraded state (a fallback budget because config.json
// was missing/unparseable, or rune-clamp-only mode because tokenizer.json could not
// be loaded) so the caller can warn without disabling the provider.
func newTokenTruncator(modelDir string) (*tokenTruncator, error) {
	budget, budgetErr := readTokenBudget(modelDir)
	t := &tokenTruncator{budget: budget, clamp: budget * runeClampBudgetFactor}

	tkBytes, err := os.ReadFile(filepath.Join(modelDir, "tokenizer.json"))
	if err != nil {
		return t, fmt.Errorf("token truncation degraded to rune clamp: read tokenizer.json: %w", err)
	}
	tk, err := hftokenizer.NewFromContent(nil, tkBytes)
	if err != nil {
		return t, fmt.Errorf("token truncation degraded to rune clamp: parse tokenizer.json: %w", err)
	}
	// Encode without special tokens so every returned span is a real byte span into
	// the original text; the two reserved positions cover the [CLS]/[SEP] the pipeline
	// adds later. IncludeSpans is required for EncodeWithAnnotations to populate Spans.
	if err := tk.With(api.EncodeOptions{IncludeSpans: true, AddSpecialTokens: false}); err != nil {
		return t, fmt.Errorf("token truncation degraded to rune clamp: configure tokenizer: %w", err)
	}
	t.tk = tk

	if budgetErr != nil {
		return t, fmt.Errorf("using fallback token budget %d: %w", budget, budgetErr)
	}
	return t, nil
}

// Truncate returns text unchanged when it fits the token budget, otherwise the
// longest prefix that stays within budget, cut on a token (and rune) boundary.
func (t *tokenTruncator) Truncate(text string) string {
	if t == nil || t.budget <= 0 || text == "" {
		return text
	}
	// Fast path: a WordPiece/subword token spans at least one rune (true for every
	// registered variant — MiniLM/BGE/Jina are all WordPiece), so a rune count
	// within budget guarantees the token count is too. Only longer texts pay for the
	// extra tokenizer pass; tokenization is µs–ms while inference dominates. (A
	// byte-level-BPE variant, where one rune can yield several tokens, would need
	// this invariant revisited — but the exact-tokenize path below is always correct.)
	if utf8.RuneCountInString(text) <= t.budget {
		return text
	}
	if t.tk == nil {
		return clampRunes(text, t.clamp)
	}
	enc := t.tk.EncodeWithAnnotations(text)
	if len(enc.IDs) <= t.budget {
		return text
	}
	if len(enc.Spans) < t.budget {
		// Spans unexpectedly short (tokenizer without span support) — degrade safely.
		return clampRunes(text, t.clamp)
	}
	cut := enc.Spans[t.budget-1].End
	// cut must land strictly inside the text: we only reach here with more than
	// budget tokens, so the budget-th token ends before the end. cut == len(text)
	// means a degenerate (zero-width) later span — fall back to the rune clamp
	// rather than returning the full over-budget text.
	if cut <= 0 || cut >= len(text) {
		return clampRunes(text, t.clamp)
	}
	// Defensive: token spans already align to rune boundaries, but never hand back a
	// string split mid-rune.
	for cut < len(text) && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// TruncateAll applies Truncate to every text. It returns the input slice
// unchanged (no allocation) when nothing needed truncating — the common case on
// this hot path — and otherwise a copy with the over-budget entries shortened.
func (t *tokenTruncator) TruncateAll(texts []string) []string {
	if t == nil || t.budget <= 0 {
		return texts
	}
	var out []string
	for i, s := range texts {
		cut := t.Truncate(s)
		if len(cut) == len(s) { // Truncate only ever returns s or a strict prefix.
			continue
		}
		if out == nil {
			out = make([]string, len(texts))
			copy(out, texts)
		}
		out[i] = cut
	}
	if out == nil {
		return texts
	}
	return out
}

// readTokenBudget derives the truncation budget from <modelDir>/config.json's
// max_position_embeddings. It returns the fallback budget with a non-nil error when
// the file is missing, unparseable, or carries an implausible value — never failing.
func readTokenBudget(modelDir string) (int, error) {
	raw, err := os.ReadFile(filepath.Join(modelDir, "config.json"))
	if err != nil {
		return fallbackTokenBudget, fmt.Errorf("read config.json: %w", err)
	}
	var cfg struct {
		MaxPositionEmbeddings int `json:"max_position_embeddings"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fallbackTokenBudget, fmt.Errorf("parse config.json: %w", err)
	}
	if cfg.MaxPositionEmbeddings <= specialTokenReserve {
		return fallbackTokenBudget, fmt.Errorf("config.json max_position_embeddings=%d is implausible", cfg.MaxPositionEmbeddings)
	}
	return cfg.MaxPositionEmbeddings - specialTokenReserve, nil
}

// clampRunes returns the longest prefix of text with at most maxRunes runes, always
// cutting on a rune boundary.
func clampRunes(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	count := 0
	for i := range text {
		if count == maxRunes {
			return text[:i]
		}
		count++
	}
	return text
}
