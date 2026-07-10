package embedding

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// wordPieceTokenizer is a minimal, dependency-free implementation of the
// HuggingFace fast-tokenizer trio used by BERT-style vocabularies:
// BertNormalizer → BertPreTokenizer → WordPiece. It loads the standard
// tokenizer.json and reproduces the exact token-ID sequences of the
// reference implementation for that configuration (verified by golden
// tests against the upstream tokenizer).
//
// Scope: only the configuration the bundled static code-embedding model
// uses — BertNormalizer{clean_text, handle_chinese_chars, lowercase,
// strip_accents:null} + BertPreTokenizer + WordPiece{"##"} — is
// implemented. Loading a tokenizer.json with a different model type
// fails loudly rather than mis-tokenizing quietly.
type wordPieceTokenizer struct {
	vocab            map[string]int
	unkID            int
	contPrefix       string
	maxWordChars     int
	lowercase        bool
	stripAccents     bool
	cleanText        bool
	handleCJK        bool
}

// tokenizerJSON mirrors just the fields of tokenizer.json this
// implementation consumes.
type tokenizerJSON struct {
	Normalizer *struct {
		Type         string `json:"type"`
		CleanText    *bool  `json:"clean_text"`
		HandleCJK    *bool  `json:"handle_chinese_chars"`
		StripAccents *bool  `json:"strip_accents"`
		Lowercase    *bool  `json:"lowercase"`
	} `json:"normalizer"`
	PreTokenizer *struct {
		Type string `json:"type"`
	} `json:"pre_tokenizer"`
	Model struct {
		Type                    string         `json:"type"`
		UnkToken                string         `json:"unk_token"`
		ContinuingSubwordPrefix string         `json:"continuing_subword_prefix"`
		MaxInputCharsPerWord    *int           `json:"max_input_chars_per_word"`
		Vocab                   map[string]int `json:"vocab"`
	} `json:"model"`
}

// loadWordPieceTokenizer parses a tokenizer.json from disk.
func loadWordPieceTokenizer(path string) (*wordPieceTokenizer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read tokenizer: %w", err)
	}
	var tj tokenizerJSON
	if err := json.Unmarshal(raw, &tj); err != nil {
		return nil, fmt.Errorf("parse tokenizer: %w", err)
	}
	if tj.Model.Type != "WordPiece" {
		return nil, fmt.Errorf("unsupported tokenizer model %q (want WordPiece)", tj.Model.Type)
	}
	if len(tj.Model.Vocab) == 0 {
		return nil, fmt.Errorf("tokenizer vocab is empty")
	}
	t := &wordPieceTokenizer{
		vocab:        tj.Model.Vocab,
		contPrefix:   tj.Model.ContinuingSubwordPrefix,
		maxWordChars: 100,
		// BertNormalizer defaults per the reference implementation.
		cleanText: true,
		handleCJK: true,
	}
	if t.contPrefix == "" {
		t.contPrefix = "##"
	}
	if tj.Model.MaxInputCharsPerWord != nil && *tj.Model.MaxInputCharsPerWord > 0 {
		t.maxWordChars = *tj.Model.MaxInputCharsPerWord
	}
	unk := tj.Model.UnkToken
	if unk == "" {
		unk = "[UNK]"
	}
	id, ok := t.vocab[unk]
	if !ok {
		return nil, fmt.Errorf("unk token %q not in vocab", unk)
	}
	t.unkID = id
	if n := tj.Normalizer; n != nil {
		if n.CleanText != nil {
			t.cleanText = *n.CleanText
		}
		if n.HandleCJK != nil {
			t.handleCJK = *n.HandleCJK
		}
		if n.Lowercase != nil {
			t.lowercase = *n.Lowercase
		}
		// strip_accents:null means "follow lowercase" in the reference
		// implementation; an explicit value wins.
		if n.StripAccents != nil {
			t.stripAccents = *n.StripAccents
		} else {
			t.stripAccents = t.lowercase
		}
	}
	return t, nil
}

// Encode returns the WordPiece token IDs for text. No special tokens
// are added (the model's post_processor is null).
func (t *wordPieceTokenizer) Encode(text string) []int {
	var ids []int
	for _, word := range t.preTokenize(t.normalize(text)) {
		ids = t.wordPiece(word, ids)
	}
	return ids
}

// normalize applies BertNormalizer: control-char cleanup, CJK padding,
// accent stripping (NFD, drop Mn), lowercasing.
func (t *wordPieceTokenizer) normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		if t.cleanText {
			if r == 0 || r == 0xFFFD || isBertControl(r) {
				continue
			}
			if isBertWhitespace(r) {
				b.WriteByte(' ')
				continue
			}
		}
		if t.handleCJK && isCJK(r) {
			b.WriteByte(' ')
			b.WriteRune(r)
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if t.stripAccents {
		decomposed := norm.NFD.String(out)
		var sb strings.Builder
		sb.Grow(len(decomposed))
		for _, r := range decomposed {
			if unicode.Is(unicode.Mn, r) {
				continue
			}
			sb.WriteRune(r)
		}
		out = sb.String()
	}
	if t.lowercase {
		out = strings.ToLower(out)
	}
	return out
}

// preTokenize applies BertPreTokenizer: split on whitespace, then
// isolate each punctuation rune as its own token.
func (t *wordPieceTokenizer) preTokenize(s string) []string {
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case isBertWhitespace(r):
			flush()
		case isBertPunct(r):
			flush()
			words = append(words, string(r))
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return words
}

// wordPiece runs the greedy longest-match-first sub-word split for one
// word, appending IDs to dst.
func (t *wordPieceTokenizer) wordPiece(word string, dst []int) []int {
	runes := []rune(word)
	if len(runes) > t.maxWordChars {
		return append(dst, t.unkID)
	}
	start := 0
	var pieces []int
	for start < len(runes) {
		end := len(runes)
		id := -1
		for end > start {
			sub := string(runes[start:end])
			if start > 0 {
				sub = t.contPrefix + sub
			}
			if v, ok := t.vocab[sub]; ok {
				id = v
				break
			}
			end--
		}
		if id < 0 {
			// No piece matched: the whole word becomes UNK, per the
			// reference implementation.
			return append(dst, t.unkID)
		}
		pieces = append(pieces, id)
		start = end
	}
	return append(dst, pieces...)
}

// isBertControl mirrors the reference _is_control: category Cc/Cf,
// except tab / newline / carriage-return which count as whitespace.
func isBertControl(r rune) bool {
	if r == '\t' || r == '\n' || r == '\r' {
		return false
	}
	return unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r)
}

// isBertWhitespace mirrors the reference _is_whitespace.
func isBertWhitespace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r':
		return true
	}
	return unicode.Is(unicode.Zs, r)
}

// isBertPunct mirrors the reference _is_punctuation: the four ASCII
// symbol ranges plus every Unicode P* category rune.
func isBertPunct(r rune) bool {
	if (r >= 33 && r <= 47) || (r >= 58 && r <= 64) || (r >= 91 && r <= 96) || (r >= 123 && r <= 126) {
		return true
	}
	return unicode.IsPunct(r)
}

// isCJK mirrors the reference _is_chinese_char CJK block test.
func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF,
		r >= 0x3400 && r <= 0x4DBF,
		r >= 0x20000 && r <= 0x2A6DF,
		r >= 0x2A700 && r <= 0x2B73F,
		r >= 0x2B740 && r <= 0x2B81F,
		r >= 0x2B820 && r <= 0x2CEAF,
		r >= 0xF900 && r <= 0xFAFF,
		r >= 0x2F800 && r <= 0x2FA1F:
		return true
	}
	return false
}
