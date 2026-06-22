package resolver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zzet/gortex/internal/graph"
)

// These tests pin the canonical-definition contract for type-use /
// reference / instantiate edges: when several nodes share a name, the
// resolver must land the edge on the *canonical* in-repo definition —
// the one search_symbols returns — rather than a same-named rival
// (external stub, test/mock definition, private or nested member type).
// Each test seeds the rival shape that, before the fix, stole the edge
// and left the canonical definition with zero incoming usage (the
// "likely_unused" hard-0 for widely-imported and builder-pattern types).

// usageIncoming counts the incoming edge kinds find_usages treats as
// real usage (mirrors graph.ClassifyZeroEdge's usageEdgeKinds).
func usageIncoming(g *graph.Graph, id string) int {
	usage := map[graph.EdgeKind]bool{
		graph.EdgeCalls: true, graph.EdgeReferences: true, graph.EdgeInstantiates: true,
		graph.EdgeImplements: true, graph.EdgeExtends: true, graph.EdgeReads: true,
		graph.EdgeWrites: true, graph.EdgeTests: true,
	}
	n := 0
	for _, e := range g.GetInEdges(id) {
		if usage[e.Kind] {
			n++
		}
	}
	return n
}

// A type-use edge must land on the real in-repo definition, never on a
// same-named external / synthetic stub node.
func TestCanonicalPick_PrefersRealDefOverExternalStub(t *testing.T) {
	g := graph.New()
	// Canonical definition in a normal source file.
	canon := &graph.Node{
		ID: "repoA/client/OkHttpClient.kt::OkHttpClient", Kind: graph.KindType, Name: "OkHttpClient",
		FilePath: "repoA/client/OkHttpClient.kt", Language: "kotlin", RepoPrefix: "repoA",
		Meta: map[string]any{"visibility": "public"},
	}
	g.AddNode(canon)
	// A same-named synthetic external placeholder (re-export / external_call
	// terminal). Marked external so the ranker demotes it hard.
	g.AddNode(&graph.Node{
		ID: "repoA/external::OkHttpClient", Kind: graph.KindType, Name: "OkHttpClient",
		FilePath: "external::OkHttpClient", Language: "kotlin", RepoPrefix: "repoA",
		Meta: map[string]any{"external": true, "synthetic": true},
	})
	// The caller node fixes the edge's repo (callerRepoPrefix reads it).
	g.AddNode(&graph.Node{ID: "repoA/app/Main.kt::run", Kind: graph.KindFunction, Name: "run", FilePath: "repoA/app/Main.kt", Language: "kotlin", RepoPrefix: "repoA"})
	g.AddNode(&graph.Node{ID: "repoA/app/Main.kt::field", Kind: graph.KindVariable, Name: "field", FilePath: "repoA/app/Main.kt", Language: "kotlin", RepoPrefix: "repoA"})

	// instantiate + typed_as + references edges from another file.
	inst := &graph.Edge{From: "repoA/app/Main.kt::run", To: "unresolved::OkHttpClient", Kind: graph.EdgeInstantiates, FilePath: "repoA/app/Main.kt", Line: 5}
	typed := &graph.Edge{From: "repoA/app/Main.kt::field", To: "unresolved::OkHttpClient", Kind: graph.EdgeTypedAs, FilePath: "repoA/app/Main.kt", Line: 6}
	g.AddEdge(inst)
	g.AddEdge(typed)

	New(g).ResolveAll()

	assert.Equal(t, canon.ID, inst.To, "instantiate must land on the real definition, not the external stub")
	assert.Equal(t, canon.ID, typed.To, "typed_as must land on the real definition, not the external stub")
	assert.GreaterOrEqual(t, usageIncoming(g, canon.ID), 1, "canonical def must have incoming usage")
}

// A type-use edge must land on the non-test definition when a same-named
// type also exists in a test source file.
func TestCanonicalPick_PrefersNonTestOverTestDef(t *testing.T) {
	g := graph.New()
	canon := &graph.Node{
		ID: "repoA/src/main/Response.kt::Response", Kind: graph.KindType, Name: "Response",
		FilePath: "repoA/src/main/Response.kt", Language: "kotlin", RepoPrefix: "repoA",
		Meta: map[string]any{"visibility": "public"},
	}
	g.AddNode(canon)
	// Same-named class in a test source file — must not catch the edge.
	g.AddNode(&graph.Node{
		ID: "repoA/src/jvmTest/SomeTest.kt::Response", Kind: graph.KindType, Name: "Response",
		FilePath: "repoA/src/jvmTest/SomeTest.kt", Language: "kotlin", RepoPrefix: "repoA",
		Meta: map[string]any{"visibility": "public"},
	})

	g.AddNode(&graph.Node{ID: "repoA/src/jvmTest/SomeTest.kt::testIt", Kind: graph.KindFunction, Name: "testIt", FilePath: "repoA/src/jvmTest/SomeTest.kt", Language: "kotlin", RepoPrefix: "repoA"})

	// The reference edge originates *from* the test file's directory, so a
	// naive same-directory preference would have stolen it for the test def.
	ref := &graph.Edge{From: "repoA/src/jvmTest/SomeTest.kt::testIt", To: "unresolved::Response", Kind: graph.EdgeReferences, FilePath: "repoA/src/jvmTest/SomeTest.kt", Line: 9}
	g.AddEdge(ref)

	New(g).ResolveAll()

	assert.Equal(t, canon.ID, ref.To, "reference must land on the non-test definition even when the caller is in a test file")
	assert.GreaterOrEqual(t, usageIncoming(g, canon.ID), 1)
}

// A type-use edge must prefer an exported/public definition over a
// same-named private one.
func TestCanonicalPick_PrefersExportedOverPrivate(t *testing.T) {
	g := graph.New()
	pub := &graph.Node{
		ID: "repoA/api/AppState.ts::AppState", Kind: graph.KindInterface, Name: "AppState",
		FilePath: "repoA/api/AppState.ts", Language: "typescript", RepoPrefix: "repoA",
		Meta: map[string]any{"visibility": "public"},
	}
	g.AddNode(pub)
	g.AddNode(&graph.Node{
		ID: "repoA/internal/local.ts::AppState", Kind: graph.KindInterface, Name: "AppState",
		FilePath: "repoA/internal/local.ts", Language: "typescript", RepoPrefix: "repoA",
		Meta: map[string]any{"visibility": "private"},
	})

	g.AddNode(&graph.Node{ID: "repoA/app/use.ts::handler", Kind: graph.KindFunction, Name: "handler", FilePath: "repoA/app/use.ts", Language: "typescript", RepoPrefix: "repoA"})
	typed := &graph.Edge{From: "repoA/app/use.ts::handler", To: "unresolved::AppState", Kind: graph.EdgeTypedAs, FilePath: "repoA/app/use.ts", Line: 3}
	g.AddEdge(typed)

	New(g).ResolveAll()

	assert.Equal(t, pub.ID, typed.To, "typed_as must prefer the exported definition over a private same-named one")
}

// The nested-builder shape: a reference / instantiate of `Foo` must not
// be stolen by a nested `Foo.Builder` member type, and an instantiate of
// the nested `Foo.Builder` must not be stolen by the top-level `Foo`.
func TestCanonicalPick_NestedBuilderDoesNotStealParentEdges(t *testing.T) {
	g := graph.New()
	// Top-level Foo.
	foo := &graph.Node{
		ID: "repoA/Foo.kt::Foo", Kind: graph.KindType, Name: "Foo",
		FilePath: "repoA/Foo.kt", Language: "kotlin", RepoPrefix: "repoA",
		Meta: map[string]any{"visibility": "public"},
	}
	g.AddNode(foo)
	// Nested Foo.Builder, expressed as a dotted name (the qualified form a
	// language that keeps the enclosing-type prefix emits).
	builder := &graph.Node{
		ID: "repoA/Foo.kt::Foo.Builder", Kind: graph.KindType, Name: "Foo.Builder",
		FilePath: "repoA/Foo.kt", Language: "kotlin", RepoPrefix: "repoA",
		Meta: map[string]any{"visibility": "public"},
	}
	g.AddNode(builder)
	g.AddNode(&graph.Node{ID: "repoA/app/Main.kt::run", Kind: graph.KindFunction, Name: "run", FilePath: "repoA/app/Main.kt", Language: "kotlin", RepoPrefix: "repoA"})

	// A reference to `Foo` must land on the top-level Foo, not Foo.Builder.
	refFoo := &graph.Edge{From: "repoA/app/Main.kt::run", To: "unresolved::Foo", Kind: graph.EdgeReferences, FilePath: "repoA/app/Main.kt", Line: 4}
	g.AddEdge(refFoo)
	// An instantiate of the nested `Foo.Builder` must land on the builder,
	// not on the top-level Foo.
	instBuilder := &graph.Edge{From: "repoA/app/Main.kt::run", To: "unresolved::Foo.Builder", Kind: graph.EdgeInstantiates, FilePath: "repoA/app/Main.kt", Line: 5}
	g.AddEdge(instBuilder)

	New(g).ResolveAll()

	assert.Equal(t, foo.ID, refFoo.To, "reference to Foo must not be stolen by the nested Foo.Builder")
	assert.Equal(t, builder.ID, instBuilder.To, "instantiate of Foo.Builder must land on the nested builder, not on Foo")
	assert.GreaterOrEqual(t, usageIncoming(g, foo.ID), 1, "top-level Foo must keep its own incoming usage")
	assert.GreaterOrEqual(t, usageIncoming(g, builder.ID), 1, "nested Foo.Builder must keep its own incoming usage")
}

// Guard: with no rival, the single canonical type still resolves (the
// fix must not narrow the common one-candidate case).
func TestCanonicalPick_SingleCandidateStillResolves(t *testing.T) {
	g := graph.New()
	canon := &graph.Node{
		ID: "repoA/Widget.kt::Widget", Kind: graph.KindType, Name: "Widget",
		FilePath: "repoA/Widget.kt", Language: "kotlin", RepoPrefix: "repoA",
	}
	g.AddNode(canon)
	g.AddNode(&graph.Node{ID: "repoA/app/Main.kt::run", Kind: graph.KindFunction, Name: "run", FilePath: "repoA/app/Main.kt", Language: "kotlin", RepoPrefix: "repoA"})
	inst := &graph.Edge{From: "repoA/app/Main.kt::run", To: "unresolved::Widget", Kind: graph.EdgeInstantiates, FilePath: "repoA/app/Main.kt", Line: 2}
	g.AddEdge(inst)

	New(g).ResolveAll()

	assert.Equal(t, canon.ID, inst.To)
}
