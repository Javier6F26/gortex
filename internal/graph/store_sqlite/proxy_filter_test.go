package store_sqlite

import (
	"path/filepath"
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

// TestProxyNodes_NeverReachDisk validates that cross-daemon proxy-edge
// nodes (and their edges) are dropped at the single durable write boundary
// (AddNode / AddBatch), so a warm restart over the store never sees them,
// while every real node round-trips intact.
func TestProxyNodes_NeverReachDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	real := &graph.Node{ID: "local/a.go::Foo", Kind: graph.KindFunction, Name: "Foo"}
	proxy := &graph.Node{
		ID:     graph.ProxyNodeID("remoteB", "rb/x.go::Bar"),
		Kind:   graph.KindFunction, Name: "Bar",
		Origin: "remote:remoteB", Stub: true,
	}
	proxyEdge := &graph.Edge{From: real.ID, To: proxy.ID, Kind: graph.EdgeCalls}

	// Mix proxy + real through AddBatch...
	s.AddBatch([]*graph.Node{real, proxy}, []*graph.Edge{proxyEdge})
	// ...and a proxy through the per-node path.
	s.AddNode(&graph.Node{
		ID:     graph.ProxyNodeID("remoteC", "rc/y.go::Baz"),
		Kind:   graph.KindFunction, Name: "Baz",
		Origin: "remote:remoteC", Stub: true,
	})
	_ = s.Close()

	// Reopen — a warm restart sees the durable store only.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = s2.Close() }()

	if s2.GetNode(proxy.ID) != nil {
		t.Error("proxy node must not be persisted")
	}
	if s2.GetNode(graph.ProxyNodeID("remoteC", "rc/y.go::Baz")) != nil {
		t.Error("proxy node added via AddNode must not be persisted")
	}
	if s2.GetNode(real.ID) == nil {
		t.Error("the real node must round-trip intact")
	}
	if outs := s2.GetOutEdges(real.ID); len(outs) != 0 {
		t.Errorf("the edge to a proxy node must not be persisted; got %d", len(outs))
	}
}
