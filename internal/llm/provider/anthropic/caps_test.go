package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zzet/gortex/internal/llm"
)

func TestSupportsEffortLevel(t *testing.T) {
	cases := []struct {
		model string
		level string
		want  bool
	}{
		{"claude-opus-4-8", "high", true},
		{"claude-opus-4-8", "max", true},
		{"claude-opus-4-8", "xhigh", true},
		{"claude-sonnet-4-6", "max", true},
		{"claude-sonnet-4-6", "xhigh", false}, // xhigh is opus-4-7/4-8 only
		{"claude-opus-4-5", "max", false},     // 4-5 tops out at high
		{"claude-opus-4-5", "high", true},
		{"claude-3-5-haiku", "low", false}, // older family — no effort knob
		{"claude-opus-4-8", "bogus", false},
	}
	for _, c := range cases {
		if got := supportsEffortLevel(c.model, c.level); got != c.want {
			t.Errorf("supportsEffortLevel(%q,%q)=%v want %v", c.model, c.level, got, c.want)
		}
	}
}

// captureBody runs one Complete against a mock and returns the decoded
// request body.
func captureBody(t *testing.T, cfg llm.RemoteConfig) map[string]any {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"ok"}]}`)
	}))
	defer srv.Close()

	t.Setenv("ANTHROPIC_API_KEY", "k")
	cfg.BaseURL = srv.URL
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	if _, err := p.Complete(context.Background(), llm.CompletionRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestComplete_SendsOutputConfigForSupportedEffort(t *testing.T) {
	got := captureBody(t, llm.RemoteConfig{Model: "claude-opus-4-8", Effort: "high"})
	oc, ok := got["output_config"].(map[string]any)
	if !ok {
		t.Fatalf("expected output_config in the request, got %v", got["output_config"])
	}
	if oc["effort"] != "high" {
		t.Errorf("output_config.effort=%v want high", oc["effort"])
	}
}

func TestComplete_OmitsOutputConfigForUnsupportedEffort(t *testing.T) {
	// xhigh is not valid for sonnet — it must be suppressed, not sent.
	got := captureBody(t, llm.RemoteConfig{Model: "claude-sonnet-4-6", Effort: "xhigh"})
	if _, ok := got["output_config"]; ok {
		t.Errorf("unsupported effort must not be sent, got %v", got["output_config"])
	}
}

func TestComplete_NoEffortByDefault(t *testing.T) {
	got := captureBody(t, llm.RemoteConfig{Model: "claude-opus-4-8"})
	if _, ok := got["output_config"]; ok {
		t.Error("effort is off by default — no output_config should be sent")
	}
}
