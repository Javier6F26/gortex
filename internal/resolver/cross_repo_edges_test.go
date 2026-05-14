package resolver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zzet/gortex/internal/graph"
)

// countOutEdgesByKind returns how many out-edges of the given kind the
// node fromID has.
func countOutEdgesByKind(g *graph.Graph, fromID string, kind graph.EdgeKind) int {
	n := 0
	for _, e := range g.GetOutEdges(fromID) {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// firstOutEdgeByKind returns the first out-edge of fromID with the given
// kind, or nil.
func firstOutEdgeByKind(g *graph.Graph, fromID string, kind graph.EdgeKind) *graph.Edge {
	for _, e := range g.GetOutEdges(fromID) {
		if e.Kind == kind {
			return e
		}
	}
	return nil
}

func TestDetectCrossRepoEdges_CallsAcrossRepos(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{ID: "repoA/a.go::Caller", Kind: graph.KindFunction, Name: "Caller", FilePath: "repoA/a.go", RepoPrefix: "repoA"})
	g.AddNode(&graph.Node{ID: "repoB/b.go::Callee", Kind: graph.KindFunction, Name: "Callee", FilePath: "repoB/b.go", RepoPrefix: "repoB"})

	base := &graph.Edge{
		From: "repoA/a.go::Caller", To: "repoB/b.go::Callee",
		Kind: graph.EdgeCalls, FilePath: "repoA/a.go", Line: 7,
		Confidence: 0.8, Origin: graph.OriginASTInferred,
	}
	g.AddEdge(base)

	emitted := DetectCrossRepoEdges(g)
	assert.Equal(t, 1, emitted)

	// Parallel edge materialised.
	cr := firstOutEdgeByKind(g, "repoA/a.go::Caller", graph.EdgeCrossRepoCalls)
	if assert.NotNil(t, cr, "expected a cross_repo_calls edge") {
		assert.Equal(t, "repoB/b.go::Callee", cr.To)
		assert.Equal(t, "repoA/a.go", cr.FilePath)
		assert.Equal(t, 7, cr.Line)
		// Origin / confidence inherited from the base edge.
		assert.Equal(t, graph.OriginASTInferred, cr.Origin)
		assert.Equal(t, 0.8, cr.Confidence)
		assert.True(t, cr.CrossRepo)
		assert.Equal(t, "calls", cr.Meta["base_kind"])
		assert.Equal(t, "repoA", cr.Meta["source_repo"])
		assert.Equal(t, "repoB", cr.Meta["target_repo"])
	}

	// Base edge keeps its kind and now carries the bool flag.
	assert.Equal(t, graph.EdgeCalls, base.Kind)
	assert.True(t, base.CrossRepo)
}

func TestDetectCrossRepoEdges_ImplementsAndExtends(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{ID: "repoA/a.go::Impl", Kind: graph.KindType, Name: "Impl", FilePath: "repoA/a.go", RepoPrefix: "repoA"})
	g.AddNode(&graph.Node{ID: "repoB/b.go::Iface", Kind: graph.KindInterface, Name: "Iface", FilePath: "repoB/b.go", RepoPrefix: "repoB"})
	g.AddNode(&graph.Node{ID: "repoA/a.go::Child", Kind: graph.KindType, Name: "Child", FilePath: "repoA/a.go", RepoPrefix: "repoA"})
	g.AddNode(&graph.Node{ID: "repoB/b.go::Parent", Kind: graph.KindType, Name: "Parent", FilePath: "repoB/b.go", RepoPrefix: "repoB"})

	g.AddEdge(&graph.Edge{From: "repoA/a.go::Impl", To: "repoB/b.go::Iface", Kind: graph.EdgeImplements, FilePath: "repoA/a.go", Line: 3, Origin: graph.OriginASTResolved})
	g.AddEdge(&graph.Edge{From: "repoA/a.go::Child", To: "repoB/b.go::Parent", Kind: graph.EdgeExtends, FilePath: "repoA/a.go", Line: 4, Origin: graph.OriginASTResolved})

	emitted := DetectCrossRepoEdges(g)
	assert.Equal(t, 2, emitted)

	assert.Equal(t, 1, countOutEdgesByKind(g, "repoA/a.go::Impl", graph.EdgeCrossRepoImplements))
	assert.Equal(t, 1, countOutEdgesByKind(g, "repoA/a.go::Child", graph.EdgeCrossRepoExtends))
}

func TestDetectCrossRepoEdges_SameRepoUntouched(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{ID: "repoA/a.go::Caller", Kind: graph.KindFunction, Name: "Caller", FilePath: "repoA/a.go", RepoPrefix: "repoA"})
	g.AddNode(&graph.Node{ID: "repoA/b.go::Callee", Kind: graph.KindFunction, Name: "Callee", FilePath: "repoA/b.go", RepoPrefix: "repoA"})

	base := &graph.Edge{From: "repoA/a.go::Caller", To: "repoA/b.go::Callee", Kind: graph.EdgeCalls, FilePath: "repoA/a.go", Line: 1}
	g.AddEdge(base)

	emitted := DetectCrossRepoEdges(g)
	assert.Equal(t, 0, emitted)
	assert.Equal(t, 0, countOutEdgesByKind(g, "repoA/a.go::Caller", graph.EdgeCrossRepoCalls))
	assert.False(t, base.CrossRepo)
}

func TestDetectCrossRepoEdges_SkipsStubsAndUnstampedNodes(t *testing.T) {
	g := graph.New()
	// Caller in repoA; one edge to an unresolved stub, one to a node
	// whose RepoPrefix was never stamped.
	g.AddNode(&graph.Node{ID: "repoA/a.go::Caller", Kind: graph.KindFunction, Name: "Caller", FilePath: "repoA/a.go", RepoPrefix: "repoA"})
	g.AddNode(&graph.Node{ID: "loose.go::Loose", Kind: graph.KindFunction, Name: "Loose", FilePath: "loose.go"}) // no RepoPrefix

	g.AddEdge(&graph.Edge{From: "repoA/a.go::Caller", To: "unresolved::Missing", Kind: graph.EdgeCalls, FilePath: "repoA/a.go", Line: 1})
	g.AddEdge(&graph.Edge{From: "repoA/a.go::Caller", To: "external::net/http", Kind: graph.EdgeCalls, FilePath: "repoA/a.go", Line: 2})
	g.AddEdge(&graph.Edge{From: "repoA/a.go::Caller", To: "loose.go::Loose", Kind: graph.EdgeCalls, FilePath: "repoA/a.go", Line: 3})

	emitted := DetectCrossRepoEdges(g)
	assert.Equal(t, 0, emitted)
	assert.Equal(t, 0, countOutEdgesByKind(g, "repoA/a.go::Caller", graph.EdgeCrossRepoCalls))
}

func TestDetectCrossRepoEdges_Idempotent(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{ID: "repoA/a.go::Caller", Kind: graph.KindFunction, Name: "Caller", FilePath: "repoA/a.go", RepoPrefix: "repoA"})
	g.AddNode(&graph.Node{ID: "repoB/b.go::Callee", Kind: graph.KindFunction, Name: "Callee", FilePath: "repoB/b.go", RepoPrefix: "repoB"})
	g.AddEdge(&graph.Edge{From: "repoA/a.go::Caller", To: "repoB/b.go::Callee", Kind: graph.EdgeCalls, FilePath: "repoA/a.go", Line: 7})

	first := DetectCrossRepoEdges(g)
	second := DetectCrossRepoEdges(g)
	assert.Equal(t, 1, first)
	// Re-running re-counts the same relationship but graph.AddEdge
	// dedupes by edgeKey — the parallel edge is not duplicated.
	assert.Equal(t, 1, second)
	assert.Equal(t, 1, countOutEdgesByKind(g, "repoA/a.go::Caller", graph.EdgeCrossRepoCalls))
}

// The pass must not feed on its own output: a cross_repo_* edge is not
// itself a base kind, so a second pass finds nothing new from it.
func TestDetectCrossRepoEdges_DoesNotRecurseOnOwnOutput(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{ID: "repoA/a.go::Caller", Kind: graph.KindFunction, Name: "Caller", FilePath: "repoA/a.go", RepoPrefix: "repoA"})
	g.AddNode(&graph.Node{ID: "repoB/b.go::Callee", Kind: graph.KindFunction, Name: "Callee", FilePath: "repoB/b.go", RepoPrefix: "repoB"})
	g.AddEdge(&graph.Edge{From: "repoA/a.go::Caller", To: "repoB/b.go::Callee", Kind: graph.EdgeCalls, FilePath: "repoA/a.go", Line: 7})

	DetectCrossRepoEdges(g)
	// Exactly one base + one parallel edge — never a cross_repo_calls
	// parallel of a cross_repo_calls edge.
	assert.Equal(t, 1, countOutEdgesByKind(g, "repoA/a.go::Caller", graph.EdgeCalls))
	assert.Equal(t, 1, countOutEdgesByKind(g, "repoA/a.go::Caller", graph.EdgeCrossRepoCalls))
}
