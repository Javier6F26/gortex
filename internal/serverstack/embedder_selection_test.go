package serverstack

import (
	"strings"
	"testing"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/embedding"
)

func boolPtr(b bool) *bool { return &b }

// clearEmbeddingEnv neutralises the flag/env overrides so a test exercises the
// config-driven ResolveEmbedder path deterministically.
func clearEmbeddingEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"GORTEX_EMBEDDINGS", "GORTEX_EMBEDDINGS_URL", "GORTEX_EMBEDDINGS_MODEL", "GORTEX_EMBEDDINGS_VARIANT"} {
		t.Setenv(k, "")
	}
}

func TestResolveEmbedder_StaticDescIsTruthful(t *testing.T) {
	clearEmbeddingEnv(t)
	cfg := &config.Config{Embedding: config.EmbeddingConfig{Enabled: boolPtr(true), Provider: "static"}}

	p, desc, report, err := ResolveEmbedder(EmbedderRequest{}, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected a static provider")
	}
	defer p.Close()
	if !strings.HasPrefix(desc, "static") {
		t.Fatalf("desc = %q, want it to start with \"static\"", desc)
	}
	if len(report.Attempts) != 0 {
		t.Fatalf("explicit static should record no selection attempts, got %d", len(report.Attempts))
	}
}

func TestResolveEmbedder_APIDescIsTruthful(t *testing.T) {
	clearEmbeddingEnv(t)
	cfg := &config.Config{Embedding: config.EmbeddingConfig{
		Enabled:  boolPtr(true),
		Provider: "api",
		APIURL:   "http://localhost:11434",
	}}

	p, desc, _, err := ResolveEmbedder(EmbedderRequest{}, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected an api provider")
	}
	defer p.Close()
	if _, ok := p.(*embedding.APIProvider); !ok {
		t.Fatalf("expected *embedding.APIProvider, got %T", p)
	}
	if !strings.HasPrefix(desc, "api") {
		t.Fatalf("desc = %q, want it to start with \"api\"", desc)
	}
}

// TestResolveEmbedder_LocalDegradesToStatic forces the local auto-selection to
// fall all the way through to the static fallback (offline + an empty models
// dir so no transformer can load) and asserts the description tells the truth
// and the report records the degradation.
func TestResolveEmbedder_LocalDegradesToStatic(t *testing.T) {
	clearEmbeddingEnv(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())     // absolute → uncached models dir
	t.Setenv("GORTEX_EMBEDDING_OFFLINE", "1")  // no network download

	cfg := &config.Config{Embedding: config.EmbeddingConfig{Enabled: boolPtr(true), Provider: "local"}}

	p, desc, report, err := ResolveEmbedder(EmbedderRequest{}, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected the static fallback provider")
	}
	defer p.Close()
	if report.Chosen != "static" {
		t.Fatalf("report.Chosen = %q, want \"static\"", report.Chosen)
	}
	if len(report.Attempts) == 0 {
		t.Fatal("expected the failed backend attempts to be recorded")
	}
	if !strings.Contains(desc, "static fallback") {
		t.Fatalf("desc = %q, want it to mention the static fallback", desc)
	}
}
