package analysis

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func TestComputeHITS_EmptyGraph(t *testing.T) {
	r := ComputeHITS(graph.New())
	if len(r.Authorities) != 0 || len(r.Hubs) != 0 {
		t.Errorf("empty graph should yield no scores")
	}
	if r.MaxAuth != 0 || r.MaxHub != 0 {
		t.Errorf("empty graph maxima should be 0")
	}
	if r := ComputeHITS(nil); r == nil || len(r.Authorities) != 0 {
		t.Errorf("nil graph should yield an empty result, not nil")
	}
}

// TestComputeHITS_AuthorityAndHub builds a graph where hub nodes
// converge on one authority and verifies the algorithm assigns the
// authority role to the destination and the hub role to the callers.
func TestComputeHITS_AuthorityAndHub(t *testing.T) {
	g := graph.New()
	for _, id := range []string{"auth", "h1", "h2", "h3", "leaf"} {
		g.AddNode(&graph.Node{ID: id, Kind: graph.KindFunction, Name: id})
	}
	// h1, h2, h3 all call auth -- auth is the authority, the callers
	// are the hubs. leaf is isolated.
	for _, hub := range []string{"h1", "h2", "h3"} {
		g.AddEdge(&graph.Edge{From: hub, To: "auth", Kind: graph.EdgeCalls})
	}

	r := ComputeHITS(g)

	// The destination is the top authority.
	if r.AuthorityOf("auth") <= r.AuthorityOf("h1") {
		t.Errorf("auth authority (%f) should exceed hub h1 (%f)",
			r.AuthorityOf("auth"), r.AuthorityOf("h1"))
	}
	// The callers are the hubs; the destination has near-zero hub.
	if r.HubOf("h1") <= r.HubOf("auth") {
		t.Errorf("hub h1 (%f) should exceed destination auth hub (%f)",
			r.HubOf("h1"), r.HubOf("auth"))
	}
	// MaxAuth reflects the top authority.
	if r.MaxAuth != r.AuthorityOf("auth") {
		t.Errorf("MaxAuth=%f should equal auth's score %f", r.MaxAuth, r.AuthorityOf("auth"))
	}
	// The isolated leaf scores zero on both axes.
	if r.AuthorityOf("leaf") != 0 || r.HubOf("leaf") != 0 {
		t.Errorf("isolated leaf should score 0/0, got auth=%f hub=%f",
			r.AuthorityOf("leaf"), r.HubOf("leaf"))
	}
}

// TestComputeHITS_RecursiveAuthority verifies the recursive property:
// of two nodes with the same raw in-degree, the one whose callers are
// themselves authoritative earns the higher authority score.
func TestComputeHITS_RecursiveAuthority(t *testing.T) {
	g := graph.New()
	ids := []string{
		"realAuth", "infraUtil",
		"orch1", "orch2", // hubs that call realAuth AND each other's authorities
		"scatter1", "scatter2", // low-value callers of infraUtil
		"coreA", "coreB", // authorities the orchestrators also hit
	}
	for _, id := range ids {
		g.AddNode(&graph.Node{ID: id, Kind: graph.KindFunction, Name: id})
	}
	// orch1/orch2 are strong hubs: they call realAuth plus coreA/coreB.
	for _, h := range []string{"orch1", "orch2"} {
		g.AddEdge(&graph.Edge{From: h, To: "realAuth", Kind: graph.EdgeCalls})
		g.AddEdge(&graph.Edge{From: h, To: "coreA", Kind: graph.EdgeCalls})
		g.AddEdge(&graph.Edge{From: h, To: "coreB", Kind: graph.EdgeCalls})
	}
	// scatter1/scatter2 are weak hubs: they only call infraUtil.
	for _, s := range []string{"scatter1", "scatter2"} {
		g.AddEdge(&graph.Edge{From: s, To: "infraUtil", Kind: graph.EdgeCalls})
	}

	r := ComputeHITS(g)
	// realAuth and infraUtil both have in-degree 2, but realAuth's
	// callers are strong hubs -- so realAuth wins on authority.
	if r.AuthorityOf("realAuth") <= r.AuthorityOf("infraUtil") {
		t.Errorf("realAuth authority (%f) should exceed infraUtil (%f) "+
			"despite equal in-degree -- HITS is recursive",
			r.AuthorityOf("realAuth"), r.AuthorityOf("infraUtil"))
	}
}

func TestHITSResult_NilSafe(t *testing.T) {
	var r *HITSResult
	if r.AuthorityOf("x") != 0 || r.HubOf("x") != 0 {
		t.Error("nil HITSResult accessors should return 0")
	}
}
