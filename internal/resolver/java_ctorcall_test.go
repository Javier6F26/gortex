package resolver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zzet/gortex/internal/graph"
)

// A `new Foo(arg)` constructor-call candidate must bind to the class's explicit
// constructor node, and a `new Bar()` whose class has only an implicit default
// constructor (no `<init>` node) must stay unresolved rather than latching onto
// an unrelated class's constructor.
func TestResolveMethodCall_JavaConstructorCall(t *testing.T) {
	g := graph.New()
	fooFile := "src/main/java/org/example/PetTypeFormatter.java"
	testFile := "src/test/java/org/example/PetTypeFormatterTests.java"
	g.AddNode(&graph.Node{ID: fooFile, Kind: graph.KindFile, Name: "PetTypeFormatter.java", FilePath: fooFile, Language: "java"})
	g.AddNode(&graph.Node{ID: testFile, Kind: graph.KindFile, Name: "PetTypeFormatterTests.java", FilePath: testFile, Language: "java"})
	g.AddNode(&graph.Node{ID: fooFile + "::PetTypeFormatter", Kind: graph.KindType, Name: "PetTypeFormatter", FilePath: fooFile, Language: "java", Meta: map[string]any{"scope_pkg": "org.example"}})
	// Explicit constructor node: flat name `<Class>.<init>`, receiver meta.
	g.AddNode(&graph.Node{ID: fooFile + "::PetTypeFormatter.<init>", Kind: graph.KindMethod, Name: "PetTypeFormatter.<init>", FilePath: fooFile, Language: "java", Meta: map[string]any{"receiver": "PetTypeFormatter", "scope_pkg": "org.example"}})
	g.AddNode(&graph.Node{ID: testFile + "::PetTypeFormatterTests.shouldFormat", Kind: graph.KindMethod, Name: "shouldFormat", FilePath: testFile, Language: "java", Meta: map[string]any{"receiver": "PetTypeFormatterTests", "scope_pkg": "org.example"}})

	bound := &graph.Edge{From: testFile + "::PetTypeFormatterTests.shouldFormat", To: "unresolved::*.PetTypeFormatter.<init>", Kind: graph.EdgeCalls, FilePath: testFile, Line: 52, Meta: map[string]any{"receiver_type": "PetTypeFormatter", "via": "constructor"}}
	// A class with only an implicit default constructor — no `<init>` node.
	implicit := &graph.Edge{From: testFile + "::PetTypeFormatterTests.shouldFormat", To: "unresolved::*.Visit.<init>", Kind: graph.EdgeCalls, FilePath: testFile, Line: 53, Meta: map[string]any{"receiver_type": "Visit", "via": "constructor"}}
	g.AddEdge(bound)
	g.AddEdge(implicit)

	r := New(g)
	r.ResolveAll()

	assert.Equal(t, fooFile+"::PetTypeFormatter.<init>", bound.To,
		"new PetTypeFormatter() must bind to the explicit constructor node")
	assert.Equal(t, "unresolved::*.Visit.<init>", implicit.To,
		"new Visit() with no explicit constructor must stay unresolved, not latch onto another class's ctor")
}
