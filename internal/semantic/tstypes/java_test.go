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

// javaMethodByArity returns the unique method node of the given name
// whose declared parameter count (counted from EdgeParamOf param nodes)
// equals want. It picks one overload out of an overload set the way the
// engine's arity filter does.
func javaMethodByArity(t *testing.T, g *graph.Graph, name string, want int) *graph.Node {
	t.Helper()
	var found *graph.Node
	for _, n := range g.FindNodesByName(name) {
		if n.Kind != graph.KindMethod {
			continue
		}
		cnt := 0
		for _, e := range g.GetInEdges(n.ID) {
			if e.Kind == graph.EdgeParamOf {
				cnt++
			}
		}
		if cnt != want {
			continue
		}
		if found != nil {
			t.Fatalf("multiple %q methods with arity %d", name, want)
		}
		found = n
	}
	if found == nil {
		t.Fatalf("no %q method with arity %d", name, want)
	}
	return found
}

// An overloaded method set is disambiguated by the call site's argument
// count: foo(int) and foo(int, int) resolve independently by arity, at
// the normal AST band — a real disambiguation, not an inference.
func TestJava_OverloadArityDisambiguates(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"a/Svc.java": `package a;

public class Svc {
    public void foo(int a) {
    }

    public void foo(int a, int b) {
    }
}
`,
		"b/App.java": `package b;

import a.Svc;

public class App {
    public void one(Svc s) {
        s.foo(1);
    }

    public void two(Svc s) {
        s.foo(1, 2);
    }
}
`,
	})
	p := NewProvider(JavaSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	foo1 := javaMethodByArity(t, g, "foo", 1)
	foo2 := javaMethodByArity(t, g, "foo", 2)

	one := nodeByNameKind(t, g, "one", graph.KindMethod)
	e1 := callEdgeTo(g, one.ID, foo1.ID)
	if e1 == nil {
		t.Fatalf("s.foo(1) did not resolve to the 1-arg overload; edges: %v", g.GetOutEdges(one.ID))
	}
	assertASTProvenance(t, e1, "java-types")
	if callEdgeTo(g, one.ID, foo2.ID) != nil {
		t.Errorf("s.foo(1) wrongly resolved to the 2-arg overload")
	}

	two := nodeByNameKind(t, g, "two", graph.KindMethod)
	e2 := callEdgeTo(g, two.ID, foo2.ID)
	if e2 == nil {
		t.Fatalf("s.foo(1, 2) did not resolve to the 2-arg overload; edges: %v", g.GetOutEdges(two.ID))
	}
	assertASTProvenance(t, e2, "java-types")
	if callEdgeTo(g, two.ID, foo1.ID) != nil {
		t.Errorf("s.foo(1, 2) wrongly resolved to the 1-arg overload")
	}
}

// Two same-name members of equal arity cannot be told apart by the call
// site's argument count, so the overload set keeps skipping.
func TestJava_OverloadSameAritySkips(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"a/Svc.java": `package a;

public class Svc {
    public void foo(int a) {
    }

    public void foo(String a) {
    }
}
`,
		"b/App.java": `package b;

import a.Svc;

public class App {
    public void one(Svc s) {
        s.foo(1);
    }
}
`,
	})
	p := NewProvider(JavaSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "one", graph.KindMethod)
	assertUntouched(t, g, caller.ID, "foo", "java-types")
}

// An argument count that matches no overload's arity resolves nothing.
func TestJava_OverloadArityNoMatchSkips(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"a/Svc.java": `package a;

public class Svc {
    public void foo(int a) {
    }

    public void foo(int a, int b) {
    }
}
`,
		"b/App.java": `package b;

import a.Svc;

public class App {
    public void one(Svc s) {
        s.foo(1, 2, 3);
    }
}
`,
	})
	p := NewProvider(JavaSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "one", graph.KindMethod)
	assertUntouched(t, g, caller.ID, "foo", "java-types")
}

// A variadic overload that could also accept the call's argument count
// makes the set ambiguous — it would shadow the fixed match — so the
// arity filter conservatively skips.
func TestJava_OverloadVariadicSkips(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"a/Svc.java": `package a;

public class Svc {
    public void foo(int a) {
    }

    public void foo(int... xs) {
    }
}
`,
		"b/App.java": `package b;

import a.Svc;

public class App {
    public void one(Svc s) {
        s.foo(1);
    }
}
`,
	})
	p := NewProvider(JavaSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "one", graph.KindMethod)
	assertUntouched(t, g, caller.ID, "foo", "java-types")
}

// A non-overloaded method is unaffected by the arity filter: a single
// same-named member resolves through the count-1 path regardless of the
// call's argument count.
func TestJava_NonOverloadedResolvesWithArgs(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"a/Svc.java": `package a;

public class Svc {
    public void bar(int a) {
    }
}
`,
		"b/App.java": `package b;

import a.Svc;

public class App {
    public void one(Svc s) {
        s.bar(1);
    }
}
`,
	})
	p := NewProvider(JavaSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "one", graph.KindMethod)
	bar := nodeByNameKind(t, g, "bar", graph.KindMethod)
	e := callEdgeTo(g, caller.ID, bar.ID)
	if e == nil {
		t.Fatalf("non-overloaded s.bar(1) did not resolve; edges: %v", g.GetOutEdges(caller.ID))
	}
	assertASTProvenance(t, e, "java-types")
}
