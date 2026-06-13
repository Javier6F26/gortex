package tstypes

import (
	"testing"

	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/graph"
)

// When a declared-supertype edge must change KIND (the C# base list emits
// `extends` to a stub that resolves to an interface), the engine must not
// mutate the existing edge's Kind in place: ReindexEdge reconstructs the
// old logical key from the already-flipped Kind, so the stub's inEdges
// bucket is never cleaned and leaks a stale reference. The fix drops the
// old edge and adds a fresh one of the correct kind.
func TestCSharp_SupertypeKindFlipLeavesNoStaleAdjacency(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"A/Greeter.cs": `namespace A {
    public interface Greeter { void Greet(); }
}
`,
		"B/Impl.cs": `namespace B {
    public class Impl : Greeter { public void Greet() {} }
}
`,
	})
	impl := nodeByNameKind(t, g, "Impl", graph.KindType)
	greeter := nodeByNameKind(t, g, "Greeter", graph.KindInterface)

	p := NewProvider(CSharpSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}

	// Exactly one supertype edge from Impl, and it is implements→Greeter.
	var supers []*graph.Edge
	for _, e := range g.GetOutEdges(impl.ID) {
		if e.Kind == graph.EdgeExtends || e.Kind == graph.EdgeImplements {
			supers = append(supers, e)
		}
	}
	if len(supers) != 1 {
		t.Fatalf("want exactly 1 supertype edge, got %d: %v", len(supers), supers)
	}
	if supers[0].Kind != graph.EdgeImplements || supers[0].To != greeter.ID {
		t.Fatalf("supertype edge = %s -> %s, want implements -> %s", supers[0].Kind, supers[0].To, greeter.ID)
	}

	// The original stub target's inEdges bucket must hold no leftover
	// reference to the (now-retargeted) edge. The in-place kind flip left
	// one behind because removeEdgeFromBucket was handed the post-flip key.
	if stale := g.GetInEdges("unresolved::Greeter"); len(stale) != 0 {
		t.Fatalf("stub unresolved::Greeter retains %d stale in-edge(s): %v", len(stale), stale)
	}

	// The resolved interface must carry exactly the one implements in-edge.
	implCount := 0
	for _, e := range g.GetInEdges(greeter.ID) {
		if e.Kind == graph.EdgeImplements {
			implCount++
		}
	}
	if implCount != 1 {
		t.Fatalf("Greeter has %d implements in-edges, want 1", implCount)
	}

	if err := g.VerifyEdgeIdentities(); err != nil {
		t.Fatalf("graph edge identities inconsistent after kind flip: %v", err)
	}
}

// An import hint that refutes every repo-local candidate means the real
// target is an external / stdlib dependency the graph doesn't hold. The
// engine must NOT fall back to a lone same-named repo type — that mints a
// false edge shadowing the dependency.
func TestJava_ImportHintRefutingAllCandidatesResolvesNothing(t *testing.T) {
	// The repo has exactly one type named Logger, in pkg `a`. App imports a
	// DIFFERENT Logger (an external `org.slf4j.Logger`), so the hint points
	// at org/slf4j, which the repo-local a/Logger.java does not satisfy.
	g, dir := buildFixture(t, map[string]string{
		"a/Logger.java": `package a;

public class Logger {
    public void info() {}
}
`,
		"b/App.java": `package b;

import org.slf4j.Logger;

public class App {
    public void run() {
        Logger log = makeLogger();
        log.info();
    }

    private Logger makeLogger() { return null; }
}
`,
	})
	p := NewProvider(JavaSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	run := nodeByNameKind(t, g, "run", graph.KindMethod)
	// info() must NOT resolve to the repo-local a/Logger.java::Logger.info:
	// the import hint refuted that candidate.
	localInfo := "a/Logger.java::Logger.info"
	if e := callEdgeTo(g, run.ID, localInfo); e != nil {
		t.Fatalf("info() falsely resolved to repo-local %s despite import hint pointing at org.slf4j", localInfo)
	}
}

// confirmAST must never DOWNGRADE an edge whose effective provenance is
// already stronger than AST-grade, even when that strength lives only in
// Meta["semantic_source"] with Origin unset (a legacy compiler-confirmed
// edge). The old guard required Origin != "" and so clobbered those edges'
// tier and semantic_source.
func TestConfirmAST_DoesNotDowngradeLegacyCompilerConfirmedEdge(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"a/Svc.java": javaSvc,
		"b/App.java": `package b;

import a.Svc;

public class App {
    public void handle(Svc s) {
        s.run();
    }
}
`,
	})
	caller := nodeByNameKind(t, g, "handle", graph.KindMethod)
	target := nodeByNameKind(t, g, "run", graph.KindMethod)

	// Simulate a legacy compiler-grade edge: a resolved calls edge whose
	// strength is recorded only in semantic_source, with Origin unset
	// (edges minted before Origin stamping). Its effective origin is
	// lsp_resolved (rank 6).
	g.AddEdge(&graph.Edge{
		From:            caller.ID,
		To:              target.ID,
		Kind:            graph.EdgeCalls,
		FilePath:        "b/App.java",
		Line:            7,
		Confidence:      1.0,
		ConfidenceLabel: "EXTRACTED",
		Origin:          "", // legacy: tier lives in Meta only
		Meta:            map[string]any{"semantic_source": "java-lsp"},
	})

	a := newApplier(g, JavaSpec(), "java-types")
	e := callEdgeTo(g, caller.ID, target.ID)
	if e == nil {
		t.Fatal("seed edge missing")
	}
	if a.confirmAST(e) {
		t.Fatalf("confirmAST reported a change on a stronger-than-AST edge")
	}
	if got := effectiveOrigin(e); graph.OriginRank(got) < graph.OriginRank(graph.OriginLSPResolved) {
		t.Fatalf("effective origin downgraded to %q (rank %d), want >= lsp_resolved", got, graph.OriginRank(got))
	}
	if e.Meta["semantic_source"] != "java-lsp" {
		t.Fatalf("semantic_source clobbered to %v, want java-lsp", e.Meta["semantic_source"])
	}

	_ = dir
}
