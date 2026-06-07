package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zzet/gortex/internal/graph"
)

func declRemote(t *testing.T, declJSON string, caps []string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "indexed": true,
			"schema_version": localSchemaMajor, "api_version": 1, "read_only": true,
			"capabilities": caps,
		})
	})
	mux.HandleFunc("/v1/tools/find_declaration", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(envelope(declJSON))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func proberFor(remotes []ServerEntry) *ProxyEdgeProber {
	fed := NewFederator(FederationConfig{
		PerRemoteTimeout: 250 * time.Millisecond,
		Budget:           2 * time.Second,
		HealthTTL:        time.Millisecond,
	}, func(e ServerEntry) (*ServerClient, error) { return NewServerClient(e) }, nil)
	return NewProxyEdgeProber(fed, func() []ServerEntry { return remotes }, 250*time.Millisecond, nil)
}

const helperDeclJSON = `{"declarations":[{"declaration":{"id":"rb/lib/c.go::Helper","kind":"function","name":"Helper","file_path":"rb/lib/c.go","start_line":12,"repo_prefix":"rb","workspace_id":"wsB"},"use_sites":[]}]}`

func TestProbeDeclaration_Hit(t *testing.T) {
	remote := declRemote(t, helperDeclJSON, []string{"events", "subgraph"})
	p := proberFor([]ServerEntry{{Slug: "remoteB", URL: remote.URL}})

	decl, ok := p.ProbeDeclaration(context.Background(), "Helper", "extmod")
	if !ok {
		t.Fatal("expected a positive declaration hit")
	}
	if decl.Slug != "remoteB" || decl.RemoteID != "rb/lib/c.go::Helper" {
		t.Errorf("wrong decl identity: %+v", decl)
	}
	if decl.Kind != graph.KindFunction || decl.RepoPrefix != "rb" || decl.Line != 12 {
		t.Errorf("decl fields wrong: %+v", decl)
	}
}

func TestProbeDeclaration_NoSubgraphCap_Skipped(t *testing.T) {
	// The remote advertises no `subgraph` capability, so proxy-edge minting is
	// skipped for it even though it would have the declaration.
	remote := declRemote(t, helperDeclJSON, []string{"events"})
	p := proberFor([]ServerEntry{{Slug: "remoteB", URL: remote.URL}})

	if _, ok := p.ProbeDeclaration(context.Background(), "Helper", "extmod"); ok {
		t.Error("a remote without the subgraph cap must not yield a hit")
	}
}

func TestProbeDeclaration_EmptyHint_NoProbe(t *testing.T) {
	remote := declRemote(t, helperDeclJSON, []string{"events", "subgraph"})
	p := proberFor([]ServerEntry{{Slug: "remoteB", URL: remote.URL}})

	if _, ok := p.ProbeDeclaration(context.Background(), "Helper", ""); ok {
		t.Error("an empty import hint must short-circuit before probing")
	}
}

func TestProbeDeclaration_NameMismatch_NoHit(t *testing.T) {
	// Remote returns a declaration named something else — not a hit.
	other := `{"declarations":[{"declaration":{"id":"rb/lib/c.go::Other","kind":"function","name":"Other"}}]}`
	remote := declRemote(t, other, []string{"events", "subgraph"})
	p := proberFor([]ServerEntry{{Slug: "remoteB", URL: remote.URL}})

	if _, ok := p.ProbeDeclaration(context.Background(), "Helper", "extmod"); ok {
		t.Error("a declaration whose name does not match must not be a hit")
	}
}

func subgraphRemote(t *testing.T, body string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/subgraph", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func hydratorFor(g graph.Store, remotes []ServerEntry) *ProxyHydrator {
	return NewProxyHydrator(g,
		func(e ServerEntry) (*ServerClient, error) { return NewServerClient(e) },
		func() []ServerEntry { return remotes },
		5*time.Minute, 1, 5000, 250*time.Millisecond, nil)
}

const helperSubgraphJSON = `{"root":{"id":"rb/lib/c.go::Helper","kind":"function","name":"Helper"},` +
	`"nodes":[{"id":"rb/lib/d.go::Sub","kind":"function","name":"Sub"}],` +
	`"edges":[{"from":"rb/lib/c.go::Helper","to":"rb/lib/d.go::Sub","kind":"calls"}],` +
	`"stats":{"schema_version":1,"truncated":false}}`

func TestHydrate_PullsRing(t *testing.T) {
	remote := subgraphRemote(t, helperSubgraphJSON)
	g := graph.New()
	proxyID := graph.ProxyNodeID("remoteB", "rb/lib/c.go::Helper")
	g.AddNode(&graph.Node{ID: proxyID, Kind: graph.KindFunction, Name: "Helper", Origin: "remote:remoteB", Stub: true})

	h := hydratorFor(g, []ServerEntry{{Slug: "remoteB", URL: remote.URL}})
	added, err := h.Hydrate(context.Background(), proxyID)
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if added != 1 {
		t.Errorf("edges added = %d, want 1", added)
	}
	subPID := graph.ProxyNodeID("remoteB", "rb/lib/d.go::Sub")
	if n := g.GetNode(subPID); n == nil || !graph.IsProxyNode(n) {
		t.Errorf("neighbour proxy should be minted; got %+v", n)
	}
	outs := g.GetOutEdges(proxyID)
	if len(outs) != 1 || outs[0].To != subPID {
		t.Errorf("ring edge wrong: %+v", outs)
	}
	if g.GetNode(proxyID).FetchedAt.IsZero() {
		t.Error("root proxy FetchedAt should be refreshed after hydration")
	}
}

func TestHydrate_FreshRing_NoOp(t *testing.T) {
	remote := subgraphRemote(t, helperSubgraphJSON)
	g := graph.New()
	proxyID := graph.ProxyNodeID("remoteB", "rb/lib/c.go::Helper")
	g.AddNode(&graph.Node{ID: proxyID, Kind: graph.KindFunction, Name: "Helper", Origin: "remote:remoteB", Stub: true, FetchedAt: time.Now()})
	g.AddEdge(&graph.Edge{From: proxyID, To: "rb/lib/d.go::Existing", Kind: graph.EdgeCalls})

	h := hydratorFor(g, []ServerEntry{{Slug: "remoteB", URL: remote.URL}})
	added, err := h.Hydrate(context.Background(), proxyID)
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if added != 0 {
		t.Errorf("a fresh, populated ring must be a no-op; added=%d", added)
	}
}

func TestEvictRemote_ForcesRehydrate(t *testing.T) {
	g := graph.New()
	proxyID := graph.ProxyNodeID("remoteB", "rb/lib/c.go::Helper")
	g.AddNode(&graph.Node{ID: proxyID, Kind: graph.KindFunction, Name: "Helper", Origin: "remote:remoteB", Stub: true, FetchedAt: time.Now()})
	// A proxy owned by a different remote must be untouched.
	otherID := graph.ProxyNodeID("remoteC", "rc/x.go::Z")
	g.AddNode(&graph.Node{ID: otherID, Kind: graph.KindFunction, Name: "Z", Origin: "remote:remoteC", Stub: true, FetchedAt: time.Now()})

	h := hydratorFor(g, nil)
	if n := h.EvictRemote("remoteB"); n != 1 {
		t.Errorf("evicted = %d, want 1", n)
	}
	if !g.GetNode(proxyID).FetchedAt.IsZero() {
		t.Error("evicted proxy must be marked stale (FetchedAt zeroed)")
	}
	if g.GetNode(otherID).FetchedAt.IsZero() {
		t.Error("a different remote's proxy must be untouched by eviction")
	}
}
