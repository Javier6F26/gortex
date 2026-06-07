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

func newSubGraphHandler(t *testing.T, g *graph.Graph) *Handler {
	t.Helper()
	srv := mcpserver.NewMCPServer("gortex-test", "0.0.1-test", mcpserver.WithToolCapabilities(false))
	return NewHandler(srv, g, "0.0.1-test", zap.NewNop())
}

// TestSubGraph_ReturnsFullNodesAndRing asserts /v1/subgraph returns FULL
// node bodies (Meta / QualName / EndLine intact, unlike the brief
// /v1/graph projection) for the root and its neighbour ring.
func TestSubGraph_ReturnsFullNodesAndRing(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{
		ID: "a/x.go::Foo", Kind: graph.KindFunction, Name: "Foo",
		QualName: "pkg.Foo", EndLine: 10, Meta: map[string]any{"sig": "func Foo()"},
	})
	g.AddNode(&graph.Node{ID: "a/x.go::Bar", Kind: graph.KindFunction, Name: "Bar"})
	g.AddEdge(&graph.Edge{From: "a/x.go::Foo", To: "a/x.go::Bar", Kind: graph.EdgeCalls})

	h := newSubGraphHandler(t, g)
	rec := httptest.NewRecorder()
	h.handleSubGraph(rec, httptest.NewRequest(http.MethodGet, "/v1/subgraph?id=a/x.go::Foo", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var resp SubGraphResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Root == nil {
		t.Fatal("root is nil")
	}
	// FULL node — the fields /v1/graph strips must survive here.
	if resp.Root.QualName != "pkg.Foo" || resp.Root.EndLine != 10 {
		t.Errorf("root must be a full node (QualName/EndLine intact): %+v", resp.Root)
	}
	if len(resp.Root.Meta) == 0 {
		t.Error("full node must retain Meta")
	}
	foundBar := false
	for _, n := range resp.Nodes {
		if n.ID == "a/x.go::Bar" {
			foundBar = true
		}
	}
	if !foundBar {
		t.Errorf("neighbour Bar should be in the ring; got %+v", resp.Nodes)
	}
	if len(resp.Edges) == 0 {
		t.Error("the calls edge should be returned")
	}
	if resp.Stats.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version = %d, want %d", resp.Stats.SchemaVersion, SchemaVersion)
	}
}

func TestSubGraph_RequiresID(t *testing.T) {
	h := newSubGraphHandler(t, graph.New())
	rec := httptest.NewRecorder()
	h.handleSubGraph(rec, httptest.NewRequest(http.MethodGet, "/v1/subgraph", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing id should be 400, got %d", rec.Code)
	}
}

func TestSubGraph_NotFound(t *testing.T) {
	h := newSubGraphHandler(t, graph.New())
	rec := httptest.NewRecorder()
	h.handleSubGraph(rec, httptest.NewRequest(http.MethodGet, "/v1/subgraph?id=nope::X", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id should be 404, got %d", rec.Code)
	}
}
