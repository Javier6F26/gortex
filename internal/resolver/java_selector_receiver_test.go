package resolver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zzet/gortex/internal/graph"
)

// A selector call `testee.triggerException()` whose method name collides with a
// method in the caller's OWN class must bind to the receiver's type, not the
// same-named enclosing-class method — even when the receiver's type lives in a
// sibling Maven directory the import-reachability filter would otherwise drop.
func TestResolveMethodCall_ReceiverTypeBeatsEnclosingClass(t *testing.T) {
	g := graph.New()
	mainF := "src/main/java/org/example/system/CrashController.java"
	testF := "src/test/java/org/example/system/CrashControllerTests.java"
	g.AddNode(&graph.Node{ID: mainF, Kind: graph.KindFile, Name: "CrashController.java", FilePath: mainF, Language: "java"})
	g.AddNode(&graph.Node{ID: testF, Kind: graph.KindFile, Name: "CrashControllerTests.java", FilePath: testF, Language: "java"})
	// The production method the receiver-typed call should bind to.
	g.AddNode(&graph.Node{ID: mainF + "::CrashController.triggerException", Kind: graph.KindMethod, Name: "triggerException", FilePath: mainF, Language: "java", Meta: map[string]any{"receiver": "CrashController", "scope_class": "CrashController"}})
	// A same-named method in the caller's own class — the wrong target.
	g.AddNode(&graph.Node{ID: testF + "::CrashControllerTests.triggerException", Kind: graph.KindMethod, Name: "triggerException", FilePath: testF, Language: "java", Meta: map[string]any{"receiver": "CrashControllerTests", "scope_class": "CrashControllerTests"}})

	// `testee.triggerException()` from the test method of the same name.
	edge := &graph.Edge{From: testF + "::CrashControllerTests.triggerException", To: "unresolved::*.triggerException", Kind: graph.EdgeCalls, FilePath: testF, Line: 37, Meta: map[string]any{"receiver_type": "CrashController"}}
	g.AddEdge(edge)

	New(g).ResolveAll()

	assert.Equal(t, mainF+"::CrashController.triggerException", edge.To,
		"a receiver-typed selector call must bind to the receiver's type, not a same-named method in the caller's own class")
}
