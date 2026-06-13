package tstypes

import (
	"testing"

	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/graph"
)

const javaSvc = `package a;

public class Svc {
    public void run() {
    }

    public void stop() {
    }
}
`

const javaIface = `package a;

public interface Greeter {
    void greet();
}
`

func TestJava_DeclaredParamTypeResolvesCall(t *testing.T) {
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
	p := NewProvider(JavaSpec(), zap.NewNop())
	res, err := p.Enrich(g, dir)
	if err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "handle", graph.KindMethod)
	target := nodeByNameKind(t, g, "run", graph.KindMethod)
	e := callEdgeTo(g, caller.ID, target.ID)
	if e == nil {
		t.Fatalf("call edge %s -> %s not resolved; edges: %v", caller.ID, target.ID, g.GetOutEdges(caller.ID))
	}
	assertASTProvenance(t, e, "java-types")
	if res.EdgesConfirmed+res.EdgesAdded == 0 {
		t.Errorf("result reported no edge work: %+v", res)
	}
}

func TestJava_ConstructorInferenceResolvesCall(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"a/Svc.java": javaSvc,
		"b/App.java": `package b;

import a.Svc;

public class App {
    public void main() {
        Svc s = new Svc();
        s.run();
    }
}
`,
	})
	p := NewProvider(JavaSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "main", graph.KindMethod)
	target := nodeByNameKind(t, g, "run", graph.KindMethod)
	if callEdgeTo(g, caller.ID, target.ID) == nil {
		t.Fatalf("constructor-inferred call not resolved; edges: %v", g.GetOutEdges(caller.ID))
	}
}

// Cross-file resolution must follow the import hint when several types
// share a name.
func TestJava_ImportHintDisambiguatesCrossFile(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"a/Svc.java": javaSvc,
		"other/Svc.java": `package other;

public class Svc {
    public void run() {
    }
}
`,
		"b/App.java": `package b;

import a.Svc;

public class App {
    public void main() {
        Svc s = new Svc();
        s.run();
    }
}
`,
	})
	p := NewProvider(JavaSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "main", graph.KindMethod)
	want := "a/Svc.java::Svc.run"
	if callEdgeTo(g, caller.ID, want) == nil {
		t.Fatalf("import-hinted call did not land on %s; edges: %v", want, g.GetOutEdges(caller.ID))
	}
	wrong := "other/Svc.java::Svc.run"
	if callEdgeTo(g, caller.ID, wrong) != nil {
		t.Fatalf("call landed on the wrong package's type %s", wrong)
	}
}

func TestJava_ImplementsAndExtendsSynthesis(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"a/Svc.java":     javaSvc,
		"a/Greeter.java": javaIface,
		"b/Impl.java": `package b;

import a.Greeter;
import a.Svc;

public class Impl extends Svc implements Greeter {
    public void greet() {
    }
}
`,
	})
	p := NewProvider(JavaSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	impl := nodeByNameKind(t, g, "Impl", graph.KindType)
	iface := nodeByNameKind(t, g, "Greeter", graph.KindInterface)
	svc := nodeByNameKind(t, g, "Svc", graph.KindType)

	ie := edgeBetween(g, impl.ID, graph.EdgeImplements, iface.ID)
	if ie == nil {
		t.Fatalf("implements edge missing; edges: %v", g.GetOutEdges(impl.ID))
	}
	assertASTProvenance(t, ie, "java-types")

	ee := edgeBetween(g, impl.ID, graph.EdgeExtends, svc.ID)
	if ee == nil {
		t.Fatalf("extends edge missing; edges: %v", g.GetOutEdges(impl.ID))
	}
	assertASTProvenance(t, ee, "java-types")
}

// Inherited methods resolve through the synthesized extends chain.
func TestJava_InheritedMethodResolvesThroughExtends(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"a/Svc.java": javaSvc,
		"b/Sub.java": `package b;

import a.Svc;

public class Sub extends Svc {
}
`,
		"c/App.java": `package c;

import b.Sub;

public class App {
    public void main() {
        Sub s = new Sub();
        s.run();
    }
}
`,
	})
	p := NewProvider(JavaSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "main", graph.KindMethod)
	want := "a/Svc.java::Svc.run"
	if callEdgeTo(g, caller.ID, want) == nil {
		t.Fatalf("inherited method call did not resolve to %s; edges: %v", want, g.GetOutEdges(caller.ID))
	}
}

// this-qualified and field-typed receivers resolve inside the class.
func TestJava_SelfAndFieldReceivers(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"a/Svc.java": javaSvc,
		"b/App.java": `package b;

import a.Svc;

public class App {
    private Svc worker;

    public void direct() {
        this.helper();
    }

    public void helper() {
        this.worker.run();
    }
}
`,
	})
	p := NewProvider(JavaSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	direct := nodeByNameKind(t, g, "direct", graph.KindMethod)
	helper := nodeByNameKind(t, g, "helper", graph.KindMethod)
	if callEdgeTo(g, direct.ID, helper.ID) == nil {
		t.Fatalf("this.helper() not resolved; edges: %v", g.GetOutEdges(direct.ID))
	}
	run := nodeByNameKind(t, g, "run", graph.KindMethod)
	if callEdgeTo(g, helper.ID, run.ID) == nil {
		t.Fatalf("this.worker.run() not resolved through field type; edges: %v", g.GetOutEdges(helper.ID))
	}
}

// A receiver rebound to a different type degrades to unknown — the
// engine must leave its calls untouched rather than guess.
func TestJava_AmbiguousReceiverStaysUntouched(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"a/Svc.java": javaSvc,
		"a/Alt.java": `package a;

public class Alt {
    public void run() {
    }
}
`,
		"b/App.java": `package b;

import a.Alt;
import a.Svc;

public class App {
    public void main() {
        Object s;
        s = new Svc();
        s = new Alt();
        s.run();
    }
}
`,
	})
	p := NewProvider(JavaSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "main", graph.KindMethod)
	assertUntouched(t, g, caller.ID, "run", "java-types")
}

func TestJava_EnrichFileScopesToOneFile(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"a/Svc.java": javaSvc,
		"b/App.java": `package b;

import a.Svc;

public class App {
    public void main() {
        Svc s = new Svc();
        s.run();
    }
}
`,
		"c/Other.java": `package c;

import a.Svc;

public class Other {
    public void go() {
        Svc s = new Svc();
        s.stop();
    }
}
`,
	})
	p := NewProvider(JavaSpec(), zap.NewNop())
	if _, err := p.EnrichFile(g, dir, "b/App.java"); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "main", graph.KindMethod)
	run := nodeByNameKind(t, g, "run", graph.KindMethod)
	if callEdgeTo(g, caller.ID, run.ID) == nil {
		t.Fatalf("EnrichFile did not resolve the target file's call")
	}
	other := nodeByNameKind(t, g, "go", graph.KindMethod)
	assertUntouched(t, g, other.ID, "stop", "java-types")
}
