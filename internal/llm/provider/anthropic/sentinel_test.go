package anthropic

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func modelsServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path=%q want /v1/models", r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestResolveModel_PassesThroughConcreteID(t *testing.T) {
	got := resolveModel("claude-opus-4-8", "key-concrete", "https://unused", http.DefaultClient)
	if got != "claude-opus-4-8" {
		t.Errorf("a dated model id must pass through unchanged, got %q", got)
	}
}

func TestResolveModel_EnvOverrideWins(t *testing.T) {
	t.Setenv("GORTEX_LLM_ANTHROPIC_SONNET_MODEL", "claude-sonnet-9-9")
	got := resolveModel("claude-sonnet", "key-envoverride", "https://unused", http.DefaultClient)
	if got != "claude-sonnet-9-9" {
		t.Errorf("env override should win, got %q", got)
	}
}

func TestResolveModel_DiscoversNewestInTier(t *testing.T) {
	body := `{"data":[
      {"id":"claude-sonnet-4-5","created_at":"2025-09-01T00:00:00Z"},
      {"id":"claude-sonnet-4-6","created_at":"2026-01-15T00:00:00Z"},
      {"id":"claude-opus-4-8","created_at":"2026-05-01T00:00:00Z"},
      {"id":"claude-haiku-4-5-20251001","created_at":"2025-10-01T00:00:00Z"}
    ]}`
	srv := modelsServer(t, body, http.StatusOK)
	defer srv.Close()

	got := resolveModel("claude-sonnet", "key-discover-uniq", srv.URL, srv.Client())
	if got != "claude-sonnet-4-6" {
		t.Errorf("expected the newest sonnet, got %q", got)
	}
}

func TestResolveModel_FallsBackOnDiscoveryError(t *testing.T) {
	srv := modelsServer(t, `{"error":"nope"}`, http.StatusForbidden)
	defer srv.Close()

	if got := resolveModel("claude-opus", "key-fallback-uniq", srv.URL, srv.Client()); got != fallbackOpus {
		t.Errorf("expected the pinned opus fallback %q, got %q", fallbackOpus, got)
	}
	if got := resolveModel("claude-haiku", "key-fallback-uniq2", srv.URL, srv.Client()); got != fallbackHaiku {
		t.Errorf("expected the pinned haiku fallback %q, got %q", fallbackHaiku, got)
	}
}

func TestResolveModel_CachesPerKey(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-opus-4-8","created_at":"2026-05-01T00:00:00Z"}]}`))
	}))
	defer srv.Close()

	first := resolveModel("claude-opus", "key-cache-uniq", srv.URL, srv.Client())
	second := resolveModel("claude-opus", "key-cache-uniq", srv.URL, srv.Client())
	if first != "claude-opus-4-8" || second != "claude-opus-4-8" {
		t.Errorf("resolutions=%q/%q", first, second)
	}
	if calls != 1 {
		t.Errorf("expected discovery to hit the server once (then cache), got %d calls", calls)
	}
}
