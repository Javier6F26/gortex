package tokens

import "testing"

func TestScaleFromCL100K(t *testing.T) {
	// Claude scales cl100k by claudeRatio (1.35) — exact-equivalent to
	// CountFor's own encode-then-scale for this family.
	if got := ScaleFromCL100K("claude-opus-4-8", 1000); got != 1350 {
		t.Errorf("claude scale = %d, want 1350", got)
	}
	// DeepSeek scales by deepSeekRatio (1.05).
	if got := ScaleFromCL100K("deepseek-chat", 1000); got != 1050 {
		t.Errorf("deepseek scale = %d, want 1050", got)
	}
	// OpenAI-o200k and Gemini use ratio 1.0 — count passes through.
	for _, m := range []string{"gpt-4o", "gpt-4.1", "o4-mini", "gemini-2.5-pro"} {
		if got := ScaleFromCL100K(m, 1000); got != 1000 {
			t.Errorf("%s scale = %d, want 1000 (ratio 1.0)", m, got)
		}
	}
	// Unknown / empty model is a pass-through.
	if got := ScaleFromCL100K("", 1000); got != 1000 {
		t.Errorf("empty-model scale = %d, want 1000", got)
	}
	// Non-positive counts are returned unchanged.
	if got := ScaleFromCL100K("claude-opus-4-8", 0); got != 0 {
		t.Errorf("zero scale = %d, want 0", got)
	}
}
