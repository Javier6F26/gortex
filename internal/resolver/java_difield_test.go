package resolver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zzet/gortex/internal/graph"
)

// A test-tree caller invoking an interface method declared in the main tree —
// same Java package, two Maven directories, `this.<field>` receiver stamped
// with receiver_type — must resolve to the interface method node and survive
// the cross-package guard (which reverts directory-only "unreachable" edges).
func TestResolveMethodCall_JavaMavenThisFieldInterface(t *testing.T) {
	const pkg = "org.springframework.samples.petclinic.owner"
	g := graph.New()
	mainFile := "src/main/java/org/springframework/samples/petclinic/owner/OwnerRepository.java"
	testFile := "src/test/java/org/springframework/samples/petclinic/owner/OwnerControllerTests.java"
	g.AddNode(&graph.Node{ID: mainFile, Kind: graph.KindFile, Name: "OwnerRepository.java", FilePath: mainFile, Language: "java"})
	g.AddNode(&graph.Node{ID: testFile, Kind: graph.KindFile, Name: "OwnerControllerTests.java", FilePath: testFile, Language: "java"})
	g.AddNode(&graph.Node{ID: mainFile + "::OwnerRepository", Kind: graph.KindInterface, Name: "OwnerRepository", FilePath: mainFile, Language: "java", Meta: map[string]any{"scope_pkg": pkg}})
	// Flat interface method node carrying receiver meta (WS-A #2).
	g.AddNode(&graph.Node{ID: mainFile + "::findByLastNameStartingWith", Kind: graph.KindMethod, Name: "findByLastNameStartingWith", FilePath: mainFile, Language: "java", Meta: map[string]any{"receiver": "OwnerRepository", "scope_pkg": pkg}})
	g.AddNode(&graph.Node{ID: testFile + "::OwnerControllerTests.processFindFormSuccess", Kind: graph.KindMethod, Name: "processFindFormSuccess", FilePath: testFile, Language: "java", Meta: map[string]any{"receiver": "OwnerControllerTests", "scope_pkg": pkg}})

	// `this.owners.findByLastNameStartingWith(...)` — receiver_type stamped by
	// the extractor (WS-A #1).
	edge := &graph.Edge{From: testFile + "::OwnerControllerTests.processFindFormSuccess", To: "unresolved::*.findByLastNameStartingWith", Kind: graph.EdgeCalls, FilePath: testFile, Line: 94, Meta: map[string]any{"receiver_type": "OwnerRepository"}}
	g.AddEdge(edge)

	r := New(g)
	r.ResolveAll()

	assert.Equal(t, mainFile+"::findByLastNameStartingWith", edge.To,
		"same-package test→main interface method call must resolve and survive the cross-package guard")
}

// An inherited-method call — receiver typed as an in-repo class, callee
// declared two packages up as the sole in-repo definition of the name — must
// resolve via the lone-definition locality pick and not be reverted, even
// though the declaring package is never imported by name.
func TestResolveMethodCall_JavaInheritedLoneDefinition(t *testing.T) {
	g := graph.New()
	modelFile := "src/main/java/org/example/model/BaseEntity.java"
	ownerFile := "src/main/java/org/example/owner/OwnerController.java"
	g.AddNode(&graph.Node{ID: modelFile, Kind: graph.KindFile, Name: "BaseEntity.java", FilePath: modelFile, Language: "java"})
	g.AddNode(&graph.Node{ID: ownerFile, Kind: graph.KindFile, Name: "OwnerController.java", FilePath: ownerFile, Language: "java"})
	g.AddNode(&graph.Node{ID: modelFile + "::BaseEntity", Kind: graph.KindType, Name: "BaseEntity", FilePath: modelFile, Language: "java", Meta: map[string]any{"scope_pkg": "org.example.model"}})
	g.AddNode(&graph.Node{ID: modelFile + "::BaseEntity.getId", Kind: graph.KindMethod, Name: "getId", FilePath: modelFile, Language: "java", Meta: map[string]any{"receiver": "BaseEntity", "scope_pkg": "org.example.model"}})
	// The receiver's concrete type is in-repo (gate for the lone-defn keep).
	g.AddNode(&graph.Node{ID: ownerFile + "::Owner", Kind: graph.KindType, Name: "Owner", FilePath: ownerFile, Language: "java", Meta: map[string]any{"scope_pkg": "org.example.owner"}})
	g.AddNode(&graph.Node{ID: ownerFile + "::OwnerController.process", Kind: graph.KindMethod, Name: "process", FilePath: ownerFile, Language: "java", Meta: map[string]any{"receiver": "OwnerController", "scope_pkg": "org.example.owner"}})

	edge := &graph.Edge{From: ownerFile + "::OwnerController.process", To: "unresolved::*.getId", Kind: graph.EdgeCalls, FilePath: ownerFile, Line: 20, Meta: map[string]any{"receiver_type": "Owner"}}
	g.AddEdge(edge)

	r := New(g)
	r.ResolveAll()

	assert.Equal(t, modelFile+"::BaseEntity.getId", edge.To,
		"call to the sole in-repo definition of getId must resolve and survive the cross-package guard")
	assert.Equal(t, graph.OriginASTInferred, edge.Origin, "lone-candidate Java pick must land at ast_inferred grade")
}

// An external-typed receiver whose method name happens to collide with an
// unrelated in-repo method must NOT latch onto it — the guard's lone-defn
// exception is gated on the receiver naming an in-repo type.
func TestResolveMethodCall_JavaExternalReceiverStillReverts(t *testing.T) {
	g := graph.New()
	aFile := "src/main/java/org/example/a/Service.java"
	bFile := "src/main/java/org/example/b/Widget.java"
	g.AddNode(&graph.Node{ID: aFile, Kind: graph.KindFile, Name: "Service.java", FilePath: aFile, Language: "java"})
	g.AddNode(&graph.Node{ID: bFile, Kind: graph.KindFile, Name: "Widget.java", FilePath: bFile, Language: "java"})
	// Unrelated in-repo `info` method in a different package.
	g.AddNode(&graph.Node{ID: bFile + "::Widget.info", Kind: graph.KindMethod, Name: "info", FilePath: bFile, Language: "java", Meta: map[string]any{"receiver": "Widget", "scope_pkg": "org.example.b"}})
	g.AddNode(&graph.Node{ID: aFile + "::Service.run", Kind: graph.KindMethod, Name: "run", FilePath: aFile, Language: "java", Meta: map[string]any{"receiver": "Service", "scope_pkg": "org.example.a"}})

	// `logger.info(...)` — receiver_type Logger is an external facade, not an
	// in-repo type.
	edge := &graph.Edge{From: aFile + "::Service.run", To: "unresolved::*.info", Kind: graph.EdgeCalls, FilePath: aFile, Line: 5, Meta: map[string]any{"receiver_type": "Logger"}}
	g.AddEdge(edge)

	r := New(g)
	r.ResolveAll()

	assert.Equal(t, "unresolved::*.info", edge.To,
		"a call on an external-typed receiver must not latch onto an unrelated same-named in-repo method")
}
