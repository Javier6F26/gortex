package savings

import (
	"os"
	"testing"
)

func TestCostAvoided_Known(t *testing.T) {
	// Claude Opus 4.8: $5 / 1M input → 1M tokens saved = $5.
	got := CostAvoided(1_000_000, "claude-opus-4-8")
	if got < 4.99 || got > 5.01 {
		t.Errorf("CostAvoided(1M, claude-opus-4-8) = %.4f, want ≈5.00", got)
	}
	// GPT-4o-mini: $0.15 / 1M → 100k tokens saved = $0.015.
	got = CostAvoided(100_000, "gpt-4o-mini")
	if got < 0.0149 || got > 0.0151 {
		t.Errorf("CostAvoided(100k, gpt-4o-mini) = %.4f, want ≈0.015", got)
	}
	// DeepSeek chat: $0.27 / 1M → 1M tokens saved = $0.27 (cross-provider).
	got = CostAvoided(1_000_000, "deepseek-chat")
	if got < 0.2699 || got > 0.2701 {
		t.Errorf("CostAvoided(1M, deepseek-chat) = %.4f, want ≈0.27", got)
	}
}

func TestCostAvoided_FuzzyMatch(t *testing.T) {
	// Substring matches so "opus" resolves to a claude-opus-4-x row.
	if CostAvoided(1_000_000, "opus") == 0 {
		t.Error("CostAvoided with fuzzy model name 'opus' should resolve to a claude-opus row")
	}
	// Unrelated name → 0.
	if got := CostAvoided(1_000_000, "nonexistent-model"); got != 0 {
		t.Errorf("CostAvoided with unknown model = %.4f, want 0", got)
	}
}

func TestCostAvoided_ZeroOrNegative(t *testing.T) {
	if got := CostAvoided(0, "claude-opus-4"); got != 0 {
		t.Errorf("CostAvoided(0, _) = %.4f, want 0", got)
	}
	if got := CostAvoided(-100, "claude-opus-4"); got != 0 {
		t.Errorf("CostAvoided(-100, _) = %.4f, want 0", got)
	}
}

func TestCostAvoidedAll_IncludesDefaults(t *testing.T) {
	all := CostAvoidedAll(1_000_000)
	wantModels := []string{
		"claude-mythos-preview", "claude-opus-4-8", "claude-sonnet-4-6", "claude-haiku-4-5",
		"gpt-5.5", "gpt-5.4", "gpt-4.1", "gpt-4o", "o4-mini",
		"gemini-3.1-pro", "gemini-3.5-flash", "gemini-2.5-pro",
		"deepseek-chat", "deepseek-reasoner",
	}
	for _, m := range wantModels {
		if _, ok := all[m]; !ok {
			t.Errorf("CostAvoidedAll missing model %q", m)
		}
	}
}

func TestPricing_EnvOverride(t *testing.T) {
	t.Setenv("GORTEX_MODEL_PRICING_JSON", `[{"model":"testmodel","usd_per_m_input":100}]`)
	prices := Pricing()
	if len(prices) != 1 || prices[0].Model != "testmodel" || prices[0].USDPerMInput != 100 {
		t.Errorf("env override ignored: %+v", prices)
	}

	// Malformed override falls back to defaults.
	t.Setenv("GORTEX_MODEL_PRICING_JSON", "not json")
	if got := Pricing(); len(got) != len(defaultPricing) {
		t.Errorf("malformed override should fall back to defaults, got %d entries", len(got))
	}
}

func TestPricing_EnvUnsetUsesDefaults(t *testing.T) {
	_ = os.Unsetenv("GORTEX_MODEL_PRICING_JSON")
	if got := Pricing(); len(got) != len(defaultPricing) {
		t.Errorf("unset env should use defaults, got %d", len(got))
	}
}
