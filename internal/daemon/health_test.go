package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFetchHealth_ParsesAdvertisedFields asserts FetchHealth decodes the
// full advertisement, including a presence-aware read_only and the
// capability set.
func TestFetchHealth_ParsesAdvertisedFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","indexed":true,"nodes":10,"edges":20,` +
			`"version":"1.0","schema_version":1,"api_version":1,"read_only":false,` +
			`"capabilities":["events","subgraph"]}`))
	}))
	defer srv.Close()

	cli, err := NewServerClient(ServerEntry{Slug: "r2", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	h, err := cli.FetchHealth(context.Background())
	if err != nil {
		t.Fatalf("FetchHealth: %v", err)
	}
	if !h.Indexed {
		t.Error("indexed should be true")
	}
	if h.ReadOnly == nil || *h.ReadOnly {
		t.Errorf("read_only advertised false should decode to non-nil false, got %v", h.ReadOnly)
	}
	if h.EffectiveReadOnly() {
		t.Error("a remote advertising read_only:false is writable")
	}
	if !h.HasCapability("subgraph") {
		t.Error("subgraph capability should be detected")
	}
	if h.SchemaVersion != 1 || h.APIVersion != 1 {
		t.Errorf("schema/api version: got %d/%d", h.SchemaVersion, h.APIVersion)
	}
}

// TestFetchHealth_MissingReadOnlyTreatedRO asserts the fail-safe: a
// remote whose health payload omits read_only entirely (an older build)
// is treated as read-only, never assumed writable.
func TestFetchHealth_MissingReadOnlyTreatedRO(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Legacy payload: no read_only, no capabilities.
		_, _ = w.Write([]byte(`{"status":"ok","indexed":true,"nodes":1,"edges":0,"version":"0.9"}`))
	}))
	defer srv.Close()

	cli, err := NewServerClient(ServerEntry{Slug: "old", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	h, err := cli.FetchHealth(context.Background())
	if err != nil {
		t.Fatalf("FetchHealth: %v", err)
	}
	if h.ReadOnly != nil {
		t.Fatalf("absent read_only should decode to nil, got %v", *h.ReadOnly)
	}
	if !h.EffectiveReadOnly() {
		t.Error("an unadvertised read_only must be treated as read-only (fail-safe)")
	}
	if h.HasCapability("subgraph") {
		t.Error("a remote advertising no capabilities cannot have subgraph")
	}
}

// TestFetchHealth_NonOKStatus asserts an unhealthy remote yields an error
// rather than a zero-value RemoteHealth that would look writable.
func TestFetchHealth_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	cli, err := NewServerClient(ServerEntry{Slug: "down", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.FetchHealth(context.Background()); err == nil {
		t.Fatal("a 503 health must surface an error")
	}
}
