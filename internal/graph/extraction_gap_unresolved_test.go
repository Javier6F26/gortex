package graph

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A callable with no resolved incoming usages, but whose name still has
// unresolved same-name call candidates in the graph, must be classified as
// coverage-incomplete — not likely_unused. The unresolved stubs are direct
// evidence that call sites reference the name; the empty usage set is a
// resolution gap, not proof the symbol is dead.
func TestClassifyZeroEdge_UnresolvedSameNameCandidates(t *testing.T) {
	g := New()
	g.AddNode(&Node{ID: "Crash.java", Kind: KindFile, Name: "Crash.java", FilePath: "Crash.java", Language: "java"})
	g.AddNode(&Node{ID: "Crash.java::CrashController.triggerException", Kind: KindMethod, Name: "triggerException", FilePath: "Crash.java", Language: "java"})
	g.AddNode(&Node{ID: "Test.java", Kind: KindFile, Name: "Test.java", FilePath: "Test.java", Language: "java"})
	g.AddNode(&Node{ID: "Test.java::T.run", Kind: KindMethod, Name: "run", FilePath: "Test.java", Language: "java"})

	// The method is indexed (defined) but has no resolved incoming usage.
	g.AddEdge(&Edge{From: "Crash.java", To: "Crash.java::CrashController.triggerException", Kind: EdgeDefines})
	// An unresolved same-name call candidate the resolver never bound.
	g.AddEdge(&Edge{From: "Test.java::T.run", To: "unresolved::*.triggerException", Kind: EdgeCalls, FilePath: "Test.java", Line: 37})

	got := ClassifyZeroEdge(g, "Crash.java::CrashController.triggerException")
	assert.Equal(t, ZeroEdgeCoverageIncomplete, got,
		"a symbol whose name still has unresolved call candidates must not be reported likely_unused")

	// A genuinely unused method (no unresolved candidates naming it) still reads
	// as likely_unused.
	g.AddNode(&Node{ID: "Crash.java::CrashController.reallyDead", Kind: KindMethod, Name: "reallyDead", FilePath: "Crash.java", Language: "java"})
	g.AddEdge(&Edge{From: "Crash.java", To: "Crash.java::CrashController.reallyDead", Kind: EdgeDefines})
	assert.Equal(t, ZeroEdgeLikelyUnused, ClassifyZeroEdge(g, "Crash.java::CrashController.reallyDead"),
		"a method with no unresolved candidates naming it stays likely_unused")
}
