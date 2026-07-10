package embedding

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func writeTestTokenizer(t *testing.T, dir string, vocab map[string]int) {
	t.Helper()
	tj := map[string]any{
		"normalizer": map[string]any{
			"type": "BertNormalizer", "clean_text": true,
			"handle_chinese_chars": true, "strip_accents": nil, "lowercase": true,
		},
		"pre_tokenizer": map[string]any{"type": "BertPreTokenizer"},
		"model": map[string]any{
			"type": "WordPiece", "unk_token": "[UNK]",
			"continuing_subword_prefix": "##", "max_input_chars_per_word": 100,
			"vocab": vocab,
		},
	}
	raw, err := json.Marshal(tj)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, potionTokenizerFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// f32ToF16 converts float32 → IEEE half for fixture building. Handles
// the normal range the fixtures use; no rounding subtleties needed.
func f32ToF16(f float32) uint16 {
	bits := math.Float32bits(f)
	sign := uint16(bits>>16) & 0x8000
	exp := int32(bits>>23&0xff) - 127 + 15
	man := bits >> 13 & 0x3ff
	if exp <= 0 {
		return sign
	}
	if exp >= 0x1f {
		return sign | 0x7c00
	}
	return sign | uint16(exp)<<10 | uint16(man)
}

func writeTestWeights(t *testing.T, dir string, rows [][]float32) {
	t.Helper()
	dims := len(rows[0])
	header := map[string]any{
		"embeddings": map[string]any{
			"dtype": "F16", "shape": []int{len(rows), dims},
			"data_offsets": []int{0, len(rows) * dims * 2},
		},
	}
	hj, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8, 8+len(hj)+len(rows)*dims*2)
	binary.LittleEndian.PutUint64(buf, uint64(len(hj)))
	buf = append(buf, hj...)
	for _, row := range rows {
		for _, v := range row {
			var b [2]byte
			binary.LittleEndian.PutUint16(b[:], f32ToF16(v))
			buf = append(buf, b[:]...)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, potionWeightsFile), buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWordPiece_BertSchemeHermetic(t *testing.T) {
	dir := t.TempDir()
	vocab := map[string]int{
		"[PAD]": 0, "[UNK]": 1,
		"bind": 2, "##body": 3, "body": 4, "(": 5, ")": 6, "*": 7,
		"client": 8, "func": 9, ".": 10, "go": 11,
	}
	writeTestTokenizer(t, dir, vocab)
	tok, err := loadWordPieceTokenizer(filepath.Join(dir, potionTokenizerFile))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		text string
		want []int
	}{
		// camel-cased word lowercased then greedily split
		{"BindBody", []int{2, 3}},
		// punctuation isolated; unknown word → UNK
		{"func (c *Client)", []int{9, 5, 1, 7, 8, 6}},
		// dot split as punctuation
		{"bind.go", []int{2, 10, 11}},
		// whitespace collapse + empty
		{"   ", nil},
		// no matching piece anywhere → whole-word UNK
		{"zzzqqq", []int{1}},
	}
	for _, c := range cases {
		got := tok.Encode(c.text)
		if len(got) != len(c.want) {
			t.Fatalf("Encode(%q) = %v, want %v", c.text, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("Encode(%q) = %v, want %v", c.text, got, c.want)
			}
		}
	}
}

func TestPotionProvider_MeanPoolAndNormalize(t *testing.T) {
	dir := t.TempDir()
	vocab := map[string]int{"[PAD]": 0, "[UNK]": 1, "bind": 2, "##body": 3}
	writeTestTokenizer(t, dir, vocab)
	writeTestWeights(t, dir, [][]float32{
		{0, 0, 0, 0},  // PAD
		{1, 0, 0, 0},  // UNK
		{0, 2, 0, 0},  // bind
		{0, 0, 2, 0},  // ##body
	})
	p, err := NewPotionProviderFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.Dimensions() != 4 {
		t.Fatalf("dims = %d, want 4", p.Dimensions())
	}
	vec, err := p.Embed(context.Background(), "BindBody")
	if err != nil {
		t.Fatal(err)
	}
	// mean of (0,2,0,0) and (0,0,2,0) = (0,1,1,0); L2-normalised.
	want := []float32{0, float32(1 / math.Sqrt2), float32(1 / math.Sqrt2), 0}
	for i := range want {
		if math.Abs(float64(vec[i]-want[i])) > 1e-3 {
			t.Fatalf("vec = %v, want %v", vec, want)
		}
	}
	// Unknown-only text embeds the UNK row, not a zero vector.
	vec2, _ := p.Embed(context.Background(), "zzzqqq")
	if vec2[0] < 0.99 {
		t.Fatalf("UNK-only embed = %v, want unit x-axis", vec2)
	}
	// Empty text → zero vector (signal reads it as no evidence).
	vec3, _ := p.Embed(context.Background(), "")
	for _, v := range vec3 {
		if v != 0 {
			t.Fatalf("empty embed should be zero vector, got %v", vec3)
		}
	}
}

// TestPotionProvider_GoldenAgainstReference verifies the pure-Go
// tokenizer + pooling against reference outputs captured from the
// upstream tokenizers + numpy implementation for the real model.
// Skipped when the model files are not installed on this machine.
func TestPotionProvider_GoldenAgainstReference(t *testing.T) {
	dir := resolvePotionDir()
	if dir == "" {
		t.Skip("potion model files not installed; run with GORTEX_POTION_DIR pointing at the model")
	}
	p, err := NewPotionProviderFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	golden := []struct {
		text  string
		ids   []int
		head8 []float32
	}{
		{
			"BindBody bsonBinding.BindBody binding/bson.go",
			[]int{13190, 22687, 30716, 7431, 3670, 15, 13190, 22687, 7034, 16, 30716, 15, 1178},
			[]float32{0.034474, 0.018522, -0.13514, 0.084147, 0.232714, -0.36536, 0.193378, 0.139676},
		},
		{
			"decode bson request body",
			[]int{29631, 30716, 4230, 1306},
			[]float32{-0.07553, -0.022155, -0.119157, 0.177616, 0.124399, -0.224746, 0.050767, 0.065148},
		},
		{
			"func (c *Client) Do(req *Request) (*Response, error)",
			[]int{29527, 9, 42, 11, 6399, 10, 1082, 9, 29556, 11, 4230, 10, 9, 11, 2436, 13, 6564, 10},
			[]float32{-0.087694, 0.013899, -0.003767, 0.109125, -0.006322, 0.076795, 0.031422, 0.040902},
		},
	}
	for _, g := range golden {
		ids := p.tok.Encode(g.text)
		if len(ids) != len(g.ids) {
			t.Fatalf("Encode(%q) ids = %v, want %v", g.text, ids, g.ids)
		}
		for i := range ids {
			if ids[i] != g.ids[i] {
				t.Fatalf("Encode(%q) ids = %v, want %v", g.text, ids, g.ids)
			}
		}
		vec, _ := p.Embed(context.Background(), g.text)
		for i, w := range g.head8 {
			if math.Abs(float64(vec[i]-w)) > 2e-3 {
				t.Fatalf("Embed(%q)[%d] = %.6f, want %.6f", g.text, i, vec[i], w)
			}
		}
	}
}
