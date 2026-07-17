package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// makeVec returns an n-dim vector (contents irrelevant to width assertions).
func makeVec(n int) []float32 {
	v := make([]float32, n)
	for i := range v {
		v[i] = 0.1
	}
	return v
}

// TestAPIProvider_ForwardsDimensions asserts that an operator dimension
// override is sent as the OpenAI `dimensions` request parameter.
func TestAPIProvider_ForwardsDimensions(t *testing.T) {
	var gotDims int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req openAIRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotDims = req.Dimensions
		_ = json.NewEncoder(w).Encode(openAIResponse{
			Data: []openAIEmbedding{{Embedding: makeVec(req.Dimensions), Index: 0}},
		})
	}))
	defer srv.Close()

	p := NewAPIProvider(srv.URL+"/v1", "text-embedding-3-small")
	p.SetRequestedDimensions(512)

	// Override seeds Dimensions() before any embed — the probe short-circuits.
	if got := p.Dimensions(); got != 512 {
		t.Fatalf("Dimensions() before embed = %d, want 512 (override should seed it)", got)
	}

	vec, err := p.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if gotDims != 512 {
		t.Errorf("request dimensions = %d, want 512", gotDims)
	}
	if len(vec) != 512 {
		t.Errorf("vector width = %d, want 512", len(vec))
	}
}

// TestAPIProvider_OmitsDimensionsWhenUnset asserts the `dimensions` field is
// omitted (not sent as 0) when no override is set, so fixed-width models and
// providers that reject the field are unaffected.
func TestAPIProvider_OmitsDimensionsWhenUnset(t *testing.T) {
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		_ = json.NewEncoder(w).Encode(openAIResponse{
			Data: []openAIEmbedding{{Embedding: makeVec(1536), Index: 0}},
		})
	}))
	defer srv.Close()

	p := NewAPIProvider(srv.URL+"/v1", "text-embedding-3-small")
	if _, err := p.Embed(context.Background(), "hello"); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if _, present := raw["dimensions"]; present {
		t.Errorf("dimensions field present in request body without an override: %v", raw)
	}
}

// TestAPIProvider_GuardsIgnoredOverride asserts that a provider which ignores
// the requested dimensionality (returns a different width) fails the batch
// loudly rather than persisting a wrong-width vector.
func TestAPIProvider_GuardsIgnoredOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Provider ignores the requested 512 and returns 1536.
		_ = json.NewEncoder(w).Encode(openAIResponse{
			Data: []openAIEmbedding{{Embedding: makeVec(1536), Index: 0}},
		})
	}))
	defer srv.Close()

	p := NewAPIProvider(srv.URL+"/v1", "text-embedding-3-small")
	p.SetRequestedDimensions(512)

	if _, err := p.Embed(context.Background(), "hello"); err == nil {
		t.Fatal("expected an error when the provider returns a width different from the override, got nil")
	}
}

// TestAPIProvider_EmbeddingSpaceID asserts the provider identity used to detect
// a provider/model switch at startup.
func TestAPIProvider_EmbeddingSpaceID(t *testing.T) {
	openai := NewAPIProvider("https://api.openai.com/v1", "text-embedding-3-small")
	if prov, model := openai.EmbeddingSpaceID(); prov != "openai" || model != "text-embedding-3-small" {
		t.Errorf("openai EmbeddingSpaceID = (%q, %q), want (openai, text-embedding-3-small)", prov, model)
	}
	ollama := NewAPIProvider("http://localhost:11434/api", "nomic-embed-text")
	if prov, model := ollama.EmbeddingSpaceID(); prov != "ollama" || model != "nomic-embed-text" {
		t.Errorf("ollama EmbeddingSpaceID = (%q, %q), want (ollama, nomic-embed-text)", prov, model)
	}
}
