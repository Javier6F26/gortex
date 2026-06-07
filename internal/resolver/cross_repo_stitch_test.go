package resolver

import (
	"context"
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

type fakeProber struct {
	hit      bool
	decl     RemoteDecl
	calls    int
	lastName string
	lastHint string
}

func (f *fakeProber) ProbeDeclaration(_ context.Context, name, importHint string) (RemoteDecl, bool) {
	f.calls++
	f.lastName = name
	f.lastHint = importHint
	if !f.hit {
		return RemoteDecl{}, false
	}
	return f.decl, true
}

// stitchFixture builds a graph with a caller whose unresolved call to
// `Helper` has no local target. withImport controls whether the caller
// file carries an import edge (the evidence hint).
func stitchFixture(withImport bool) (*graph.Graph, *graph.Edge) {
	g := graph.New()
	g.AddNode(&graph.Node{
		ID: "repoA/pkg/a.go::Caller", Kind: graph.KindFunction, Name: "Caller",
		FilePath: "repoA/pkg/a.go", Language: "go", RepoPrefix: "repoA",
	})
	if withImport {
		wireImport(g, "repoA/pkg/a.go", "extmod", "extmod/mod.go")
	}
	edge := &graph.Edge{
		From: "repoA/pkg/a.go::Caller", To: "unresolved::Helper",
		Kind: graph.EdgeCalls, FilePath: "repoA/pkg/a.go", Line: 5,
	}
	g.AddEdge(edge)
	return g, edge
}

func hitDecl() RemoteDecl {
	return RemoteDecl{
		Slug: "remoteB", RemoteID: "rb/lib/c.go::Helper", Kind: graph.KindFunction,
		RepoPrefix: "rb", WorkspaceID: "wsB", File: "rb/lib/c.go", Line: 12,
	}
}

// (a) bare name + no import hint -> the evidence gate blocks the probe.
func TestStitch_NoImportHint_NeverProbes(t *testing.T) {
	g, edge := stitchFixture(false)
	cr := NewCrossRepo(g)
	p := &fakeProber{hit: true, decl: hitDecl()}
	cr.EnableRemoteStitch(p, 100)
	cr.ResolveAll()

	if p.calls != 0 {
		t.Errorf("with no import hint the prober must NOT be called; calls=%d", p.calls)
	}
	if edge.To != "unresolved::Helper" {
		t.Errorf("edge must stay unresolved; got %q", edge.To)
	}
}

// (b) import hint + remote miss -> probed, but no mint.
func TestStitch_ImportHint_RemoteMiss_NoMint(t *testing.T) {
	g, edge := stitchFixture(true)
	cr := NewCrossRepo(g)
	p := &fakeProber{hit: false}
	cr.EnableRemoteStitch(p, 100)
	cr.ResolveAll()

	if p.calls == 0 {
		t.Error("with an import hint the prober should be consulted")
	}
	if p.lastHint == "" {
		t.Error("the prober must receive a non-empty import hint")
	}
	if edge.To != "unresolved::Helper" {
		t.Errorf("a remote miss must not mint; edge.To=%q", edge.To)
	}
}

// (c) import hint + remote hit -> proxy minted, edge rewritten with honest
// provenance.
func TestStitch_ImportHint_RemoteHit_Mints(t *testing.T) {
	g, edge := stitchFixture(true)
	cr := NewCrossRepo(g)
	p := &fakeProber{hit: true, decl: hitDecl()}
	cr.EnableRemoteStitch(p, 100)
	cr.ResolveAll()

	pid := graph.ProxyNodeID("remoteB", "rb/lib/c.go::Helper")
	if edge.To != pid {
		t.Fatalf("edge should point at the proxy %q; got %q", pid, edge.To)
	}
	if edge.Origin != graph.OriginTextMatched {
		t.Errorf("stitched edge must be honest (text_matched), got %q", edge.Origin)
	}
	if !edge.CrossRepo {
		t.Error("stitched edge must be marked CrossRepo")
	}
	n := g.GetNode(pid)
	if n == nil || !graph.IsProxyNode(n) {
		t.Fatalf("proxy node should exist and be IsProxyNode; got %+v", n)
	}
	if n.Origin != "remote:remoteB" || !n.Stub {
		t.Errorf("proxy node fields wrong: %+v", n)
	}
}

// (d) budget exceeded -> mint refused, edge stays unresolved.
func TestStitch_BudgetExceeded_Refuses(t *testing.T) {
	g, edge := stitchFixture(true)
	// Pre-seed one proxy node so a budget of 1 is already full.
	g.AddNode(&graph.Node{
		ID: graph.ProxyNodeID("other", "o/z.go::Zzz"), Kind: graph.KindFunction,
		Name: "Zzz", Origin: "remote:other", Stub: true,
	})
	cr := NewCrossRepo(g)
	p := &fakeProber{hit: true, decl: hitDecl()}
	cr.EnableRemoteStitch(p, 1)
	cr.ResolveAll()

	if edge.To != "unresolved::Helper" {
		t.Errorf("over budget, the mint must be refused; edge.To=%q", edge.To)
	}
	if g.GetNode(graph.ProxyNodeID("remoteB", "rb/lib/c.go::Helper")) != nil {
		t.Error("no new proxy node should be minted over budget")
	}
}
