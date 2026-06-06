package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/graph"
)

func newHealthHandler(t *testing.T) *Handler {
	t.Helper()
	g := graph.New()
	srv := mcpserver.NewMCPServer("gortex-test", "0.0.1-test", mcpserver.WithToolCapabilities(false))
	return NewHandler(srv, g, "0.0.1-test", zap.NewNop())
}

// TestHealth_AdvertisesCapsAndReadOnly asserts /v1/health carries the
// federation negotiation fields a peer needs: schema_version,
// api_version, read_only, and capabilities.
func TestHealth_AdvertisesCapsAndReadOnly(t *testing.T) {
	h := newHealthHandler(t)
	h.SetReadOnly(true)
	h.SetCapabilities([]string{"events", "subgraph"})

	rec := httptest.NewRecorder()
	h.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}

	var resp HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version = %d, want %d", resp.SchemaVersion, SchemaVersion)
	}
	if resp.APIVersion != APIVersion {
		t.Errorf("api_version = %d, want %d", resp.APIVersion, APIVersion)
	}
	if !resp.ReadOnly {
		t.Error("read_only should reflect SetReadOnly(true)")
	}
	if !hasCap(resp.Capabilities, "subgraph") {
		t.Errorf("capabilities should include subgraph, got %v", resp.Capabilities)
	}

	// The raw JSON must carry the keys by name (a peer reads them off
	// the wire, not off this struct).
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"schema_version", "api_version", "read_only", "capabilities"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("health JSON missing key %q", k)
		}
	}
}

// TestHealth_DefaultPosture asserts the baseline when no posture is set:
// read_only false, capabilities default to the always-mounted event
// stream.
func TestHealth_DefaultPosture(t *testing.T) {
	h := newHealthHandler(t)
	rec := httptest.NewRecorder()
	h.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/v1/health", nil))

	var resp HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ReadOnly {
		t.Error("default read_only should be false")
	}
	if !hasCap(resp.Capabilities, "events") {
		t.Errorf("default capabilities should include events, got %v", resp.Capabilities)
	}
}

func hasCap(caps []string, want string) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}
