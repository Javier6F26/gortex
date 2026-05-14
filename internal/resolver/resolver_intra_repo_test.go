package resolver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zzet/gortex/internal/graph"
)

// These tests pin the post-fix contract of the per-repo Resolver: it
// resolves names *intra-repo only*. A name that matches solely in
// another repo is left unresolved for CrossRepoResolver — which alone
// carries the import-reachability + workspace-boundary evidence needed
// to cross a repo line. The old behaviour ("first function named X
// anywhere in the graph") is the M3 cross-repo false-positive bug.

func TestResolver_FunctionCallStaysIntraRepo(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{ID: "repoA/a.go::Caller", Kind: graph.KindFunction, Name: "Caller", FilePath: "repoA/a.go", Language: "go", RepoPrefix: "repoA"})
	// Only match for `Helper` lives in repoB.
	g.AddNode(&graph.Node{ID: "repoB/b.go::Helper", Kind: graph.KindFunction, Name: "Helper", FilePath: "repoB/b.go", Language: "go", RepoPrefix: "repoB"})

	edge := &graph.Edge{From: "repoA/a.go::Caller", To: "unresolved::Helper", Kind: graph.EdgeCalls, FilePath: "repoA/a.go", Line: 5}
	g.AddEdge(edge)

	New(g).ResolveAll()

	assert.Equal(t, "unresolved::Helper", edge.To, "per-repo Resolver must not resolve a name-only match across repos")
	assert.False(t, edge.CrossRepo)
}

func TestResolver_FunctionCallResolvesSameRepo(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{ID: "repoA/a.go::Caller", Kind: graph.KindFunction, Name: "Caller", FilePath: "repoA/a.go", Language: "go", RepoPrefix: "repoA"})
	g.AddNode(&graph.Node{ID: "repoA/b.go::Helper", Kind: graph.KindFunction, Name: "Helper", FilePath: "repoA/b.go", Language: "go", RepoPrefix: "repoA"})
	// Decoy in another repo — must be ignored.
	g.AddNode(&graph.Node{ID: "repoB/b.go::Helper", Kind: graph.KindFunction, Name: "Helper", FilePath: "repoB/b.go", Language: "go", RepoPrefix: "repoB"})

	edge := &graph.Edge{From: "repoA/a.go::Caller", To: "unresolved::Helper", Kind: graph.EdgeCalls, FilePath: "repoA/a.go", Line: 5}
	g.AddEdge(edge)

	New(g).ResolveAll()

	assert.Equal(t, "repoA/b.go::Helper", edge.To)
	assert.False(t, edge.CrossRepo)
}

// An extends edge must never resolve to a function or method — the bug
// that let `type EdgeKind string` "extend" a method named `string`.
func TestResolver_ExtendsNeverMatchesFunction(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{ID: "repoA/a.go::EdgeKind", Kind: graph.KindType, Name: "EdgeKind", FilePath: "repoA/a.go", Language: "go", RepoPrefix: "repoA"})
	// A function whose name collides with the extends target.
	g.AddNode(&graph.Node{ID: "repoA/a.go::string", Kind: graph.KindFunction, Name: "string", FilePath: "repoA/a.go", Language: "go", RepoPrefix: "repoA"})

	edge := &graph.Edge{From: "repoA/a.go::EdgeKind", To: "unresolved::string", Kind: graph.EdgeExtends, FilePath: "repoA/a.go", Line: 1}
	g.AddEdge(edge)

	New(g).ResolveAll()

	assert.Equal(t, "unresolved::string", edge.To, "extends must not land on a function candidate")
}

func TestResolver_ExtendsResolvesToSameRepoType(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{ID: "repoA/a.go::Child", Kind: graph.KindType, Name: "Child", FilePath: "repoA/a.go", Language: "go", RepoPrefix: "repoA"})
	g.AddNode(&graph.Node{ID: "repoA/b.go::Base", Kind: graph.KindType, Name: "Base", FilePath: "repoA/b.go", Language: "go", RepoPrefix: "repoA"})
	// Cross-repo type of the same name — must be ignored by the per-repo Resolver.
	g.AddNode(&graph.Node{ID: "repoB/b.go::Base", Kind: graph.KindType, Name: "Base", FilePath: "repoB/b.go", Language: "go", RepoPrefix: "repoB"})

	edge := &graph.Edge{From: "repoA/a.go::Child", To: "unresolved::Base", Kind: graph.EdgeExtends, FilePath: "repoA/a.go", Line: 1}
	g.AddEdge(edge)

	New(g).ResolveAll()

	assert.Equal(t, "repoA/b.go::Base", edge.To)
}

// InferImplements must not pair a type with a same-method-set interface
// in another repo — structural matching across repos is coincidental
// (every type with String() would "implement" every Stringer shape).
func TestInferImplements_RepoGated(t *testing.T) {
	g := graph.New()

	// repoA type with method M.
	g.AddNode(&graph.Node{ID: "repoA/a.go::Impl", Kind: graph.KindType, Name: "Impl", FilePath: "repoA/a.go", RepoPrefix: "repoA"})
	g.AddNode(&graph.Node{ID: "repoA/a.go::Impl.M", Kind: graph.KindMethod, Name: "M", FilePath: "repoA/a.go", RepoPrefix: "repoA"})
	g.AddEdge(&graph.Edge{From: "repoA/a.go::Impl.M", To: "repoA/a.go::Impl", Kind: graph.EdgeMemberOf, FilePath: "repoA/a.go", Line: 1})

	// Same-method-set interface in repoB — must NOT be paired.
	g.AddNode(&graph.Node{ID: "repoB/b.go::Iface", Kind: graph.KindInterface, Name: "Iface", FilePath: "repoB/b.go", RepoPrefix: "repoB", Meta: map[string]any{"methods": []string{"M"}}})
	// Same-method-set interface in repoA — SHOULD be paired.
	g.AddNode(&graph.Node{ID: "repoA/c.go::IfaceA", Kind: graph.KindInterface, Name: "IfaceA", FilePath: "repoA/c.go", RepoPrefix: "repoA", Meta: map[string]any{"methods": []string{"M"}}})

	added := New(g).InferImplements()
	assert.Equal(t, 1, added, "exactly one same-repo implements edge expected")

	// Verify the edge is Impl -> IfaceA (same repo), not Impl -> Iface (cross repo).
	var toCross, toSame bool
	for _, e := range g.GetOutEdges("repoA/a.go::Impl") {
		if e.Kind != graph.EdgeImplements {
			continue
		}
		switch e.To {
		case "repoB/b.go::Iface":
			toCross = true
		case "repoA/c.go::IfaceA":
			toSame = true
		}
	}
	assert.True(t, toSame, "same-repo interface must be implemented")
	assert.False(t, toCross, "cross-repo interface must NOT be implemented")
}
