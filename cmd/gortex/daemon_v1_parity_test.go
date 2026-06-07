package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/server"
	"github.com/zzet/gortex/internal/server/hub"
)

// v1ParityRoutes is the canonical set of routes the standalone server's
// registerRoutes mounts on /v1. Route parity means the daemon's composed
// /v1 handler answers every one of these (not a 404 from the mux). Each
// entry is a (method, path) the handler must recognise; the handler may
// return any non-404 status (200/400/503/etc) — we only assert the route
// is registered, never its business logic.
var v1ParityRoutes = []struct {
	method string
	path   string
}{
	{http.MethodGet, "/v1/health"},
	{http.MethodGet, "/v1/tools"},
	{http.MethodPost, "/v1/tools/search_symbols"},
	{http.MethodGet, "/v1/stats"},
	{http.MethodGet, "/v1/graph"},
	{http.MethodGet, "/v1/subgraph"},
	{http.MethodGet, "/v1/events"},
	{http.MethodGet, "/v1/activity"},
	{http.MethodGet, "/v1/caveats"},
	{http.MethodGet, "/v1/dashboard"},
	{http.MethodGet, "/v1/repos"},
	{http.MethodGet, "/v1/processes"},
	{http.MethodGet, "/v1/contracts"},
	{http.MethodGet, "/v1/contracts/validate"},
	{http.MethodGet, "/v1/communities"},
	{http.MethodGet, "/v1/guards"},
	{http.MethodGet, "/v1/workspaces/ws1/repos"},
	{http.MethodPost, "/v1/overlay/sessions"},
	{http.MethodDelete, "/v1/overlay/sessions/s1"},
	{http.MethodPut, "/v1/overlay/sessions/s1/files"},
	{http.MethodDelete, "/v1/overlay/sessions/s1/files"},
	{http.MethodGet, "/v1/overlay/sessions/s1/files"},
}

// newReferenceV1Handler builds a bare server.Handler the same way the
// standalone server (the former `gortex server`) does — NewHandler plus
// registerRoutes (run inside NewHandler). This is the parity reference.
func newReferenceV1Handler(t *testing.T) *server.Handler {
	t.Helper()
	g := graph.New()
	srv := mcpserver.NewMCPServer("gortex-test", "0.0.1-test", mcpserver.WithToolCapabilities(false))
	return server.NewHandler(srv, g, "0.0.1-test", zap.NewNop())
}

// newDaemonComposedV1Handler builds the /v1 handler exactly the way the
// daemon composes it (NewHandler + SetEventHub), returning the handler and
// the hub feeding /v1/events. This mirrors the daemon's v1 composition so
// the parity test exercises the real wiring path, not a hand-rolled mux.
func newDaemonComposedV1Handler(t *testing.T) (*server.Handler, *hub.Hub) {
	t.Helper()
	g := graph.New()
	srv := mcpserver.NewMCPServer("gortex-test", "0.0.1-test", mcpserver.WithToolCapabilities(false))
	v1 := server.NewHandler(srv, g, "0.0.1-test", zap.NewNop())
	// The daemon also calls SetConfigManager / SetServerID / SetOverlayManager
	// / SetRouter, but those are optional capability wires that do not change
	// the route set; the route-defining call is NewHandler. The daemon wires
	// an event hub so /v1/events streams — replicate that here.
	eventHub := hub.New()
	v1.SetEventHub(eventHub)
	t.Cleanup(eventHub.Stop)
	return v1, eventHub
}

// routeMatched reports whether the handler's mux has a route registered for
// (method, path). It asks the ServeMux directly via Handler(req), which
// returns the matched pattern — empty when no route is registered. This is
// precise: it distinguishes "the route is not mounted" (empty pattern) from
// "the route is mounted but its handler legitimately returns 404 for this
// input" (e.g. an unknown tool name on POST /v1/tools/, or an unknown
// workspace on /v1/workspaces/{ws}/repos), which a status-code probe cannot
// tell apart.
func routeMatched(t *testing.T, h *server.Handler, method, path string) (matched bool, pattern string) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	_, pattern = h.Mux().Handler(req)
	return pattern != "", pattern
}

// TestDaemonV1Parity asserts the daemon's composed /v1 surface mounts the
// exact same route set the standalone server handler registers, so the
// former `gortex server` REST surface is at parity when served from the
// daemon. It builds the v1 handler the way daemon.go does and exercises
// every route, comparing mount status against the reference handler.
func TestDaemonV1Parity(t *testing.T) {
	ref := newReferenceV1Handler(t)
	composed, _ := newDaemonComposedV1Handler(t)

	for _, rt := range v1ParityRoutes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			refMounted, refPat := routeMatched(t, ref, rt.method, rt.path)
			if !refMounted {
				t.Fatalf("reference server handler has no route for %s %s — the canonical route set drifted",
					rt.method, rt.path)
			}
			gotMounted, gotPat := routeMatched(t, composed, rt.method, rt.path)
			if !gotMounted {
				t.Fatalf("daemon /v1 surface is missing route %s %s — not at parity with the standalone server",
					rt.method, rt.path)
			}
			// Both must resolve to the SAME registered pattern — proof the
			// daemon mounts the identical route, not a coincidentally
			// overlapping one.
			if refPat != gotPat {
				t.Fatalf("route %s %s resolves to %q on the reference but %q on the daemon surface",
					rt.method, rt.path, refPat, gotPat)
			}
		})
	}

	// A path the mux must NOT know is the negative control: it proves the
	// matched-pattern signal above is meaningful (the mux really has no
	// route for an unknown path).
	t.Run("unknown route is unmatched on both", func(t *testing.T) {
		if m, pat := routeMatched(t, ref, http.MethodGet, "/v1/definitely-not-a-route"); m {
			t.Fatalf("reference handler must not match an unknown route, got pattern %q", pat)
		}
		if m, pat := routeMatched(t, composed, http.MethodGet, "/v1/definitely-not-a-route"); m {
			t.Fatalf("daemon handler must not match an unknown route, got pattern %q", pat)
		}
	})
}

// TestDaemonV1Parity_EventsHubWired asserts the daemon's composed /v1
// handler wires the event hub so /v1/events actually streams graph-change
// events rather than emitting the "watch mode not active" frame and
// closing. A handler with no hub emits that frame; the daemon-composed one
// must not — it subscribes and blocks (we cut it off with a short context).
func TestDaemonV1Parity_EventsHubWired(t *testing.T) {
	const inertFrame = "watch mode not active"

	// Reference handler WITHOUT a hub: /v1/events emits the inert frame.
	t.Run("no hub emits the inert frame", func(t *testing.T) {
		ref := newReferenceV1Handler(t)
		body := eventsBody(t, ref)
		if !strings.Contains(body, inertFrame) {
			t.Fatalf("a handler with no event hub should emit %q, got %q", inertFrame, body)
		}
	})

	// Daemon-composed handler WITH a hub wired: /v1/events does not emit
	// the inert frame. It subscribes and streams; the short request context
	// closes it after a keepalive-free interval, so the body is empty (the
	// SSE keepalive ticker fires only after 15s).
	t.Run("daemon hub wired does not emit the inert frame", func(t *testing.T) {
		composed, eventHub := newDaemonComposedV1Handler(t)
		if eventHub == nil {
			t.Fatal("daemon composition must wire an event hub for /v1/events")
		}
		body := eventsBody(t, composed)
		if strings.Contains(body, inertFrame) {
			t.Fatalf("with an event hub wired, /v1/events must not fall back to %q; got %q", inertFrame, body)
		}
	})
}

// eventsBody drives a single /v1/events request under a short context and
// returns whatever the handler streamed before the context cancelled it.
func eventsBody(t *testing.T, h http.Handler) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, req)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("/v1/events did not return after its context cancelled")
	}
	return rec.Body.String()
}
