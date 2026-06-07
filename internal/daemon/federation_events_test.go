package daemon

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zzet/gortex/internal/graph"
)

// TestStreamEvents_EvictsCachedProxies asserts a remote's graph_change SSE
// frame triggers eviction of this daemon's cached proxy nodes for that
// remote (marking them stale so the next access re-hydrates).
func TestStreamEvents_EvictsCachedProxies(t *testing.T) {
	g := graph.New()
	proxyID := graph.ProxyNodeID("remoteB", "rb/c.go::Helper")
	g.AddNode(&graph.Node{
		ID: proxyID, Kind: graph.KindFunction, Name: "Helper",
		Origin: "remote:remoteB", Stub: true, FetchedAt: time.Now(),
	})

	// Fake remote: /v1/events emits one graph_change frame, then closes.
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/events", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: graph_change\nid: 1\ndata: {}\n\n")
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	h := hydratorFor(g, []ServerEntry{{Slug: "remoteB", URL: srv.URL}})
	cli, err := NewServerClient(ServerEntry{Slug: "remoteB", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}

	evicted := 0
	if err := cli.StreamEvents(context.Background(), func() {
		evicted += h.EvictRemote("remoteB")
	}); err != nil {
		t.Fatalf("StreamEvents: %v", err)
	}

	if evicted == 0 {
		t.Fatal("a graph_change frame must trigger eviction")
	}
	if !g.GetNode(proxyID).FetchedAt.IsZero() {
		t.Error("the evicted proxy must be marked stale (FetchedAt zeroed) so it re-hydrates")
	}
}
