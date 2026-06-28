package tstypes

import (
	"testing"

	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/graph"
)

// A primary-constructor `val` parameter is both a constructor parameter
// and a class property, so a call through it resolves:
// `class C(val dep: Foo) { fun f() { dep.bar() } }` → dep.bar() lands on
// Foo::bar.
func TestKotlin_PrimaryCtorFieldResolvesCall(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"Foo.kt": `class Foo {
    fun bar() {}
}
`,
		"App.kt": `class C(val dep: Foo) {
    fun f() {
        dep.bar()
    }
}
`,
	})
	p := NewProvider(KotlinSpec(), zap.NewNop())
	res, err := p.Enrich(g, dir)
	if err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "f", graph.KindMethod)
	target := nodeByNameKind(t, g, "bar", graph.KindMethod)
	e := callEdgeTo(g, caller.ID, target.ID)
	if e == nil {
		t.Fatalf("primary-ctor field call %s -> %s not resolved; edges: %v", caller.ID, target.ID, g.GetOutEdges(caller.ID))
	}
	assertASTProvenance(t, e, "kotlin-types")
	if res.EdgesConfirmed+res.EdgesAdded == 0 {
		t.Errorf("result reported no edge work: %+v", res)
	}
}

// A local bound from a `Foo()` constructor call (Kotlin has no `new`)
// propagates its type to a later call: `val x = Foo(); x.bar()` → x.bar()
// resolves to Foo::bar.
func TestKotlin_ConstructorInferenceResolvesCall(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"Foo.kt": `class Foo {
    fun bar() {}
}
`,
		"App.kt": `class App {
    fun main() {
        val x = Foo()
        x.bar()
    }
}
`,
	})
	p := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "main", graph.KindMethod)
	target := nodeByNameKind(t, g, "bar", graph.KindMethod)
	e := callEdgeTo(g, caller.ID, target.ID)
	if e == nil {
		t.Fatalf("constructor-inferred call not resolved; edges: %v", g.GetOutEdges(caller.ID))
	}
	assertASTProvenance(t, e, "kotlin-types")
}

// `(Foo()).bar()` — a constructor call standing in receiver position —
// types its receiver directly from the construction.
func TestKotlin_ConstructorReceiverChainResolvesCall(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"Foo.kt": `class Foo {
    fun bar() {}
}
`,
		"App.kt": `class App {
    fun main() {
        Foo().bar()
    }
}
`,
	})
	p := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "main", graph.KindMethod)
	target := nodeByNameKind(t, g, "bar", graph.KindMethod)
	if callEdgeTo(g, caller.ID, target.ID) == nil {
		t.Fatalf("constructor-receiver chain not resolved; edges: %v", g.GetOutEdges(caller.ID))
	}
}

// A declared parameter type grounds its receiver, and a `this.field`
// access resolves through the declared property type.
func TestKotlin_ParamAndThisFieldReceivers(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"Foo.kt": `class Foo {
    fun run() {}
}
`,
		"App.kt": `class App {
    private val worker: Foo = makeFoo()

    fun direct(s: Foo) {
        s.run()
    }

    fun helper() {
        this.worker.run()
    }
}
`,
	})
	p := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	direct := nodeByNameKind(t, g, "direct", graph.KindMethod)
	helper := nodeByNameKind(t, g, "helper", graph.KindMethod)
	run := nodeByNameKind(t, g, "run", graph.KindMethod)
	if callEdgeTo(g, direct.ID, run.ID) == nil {
		t.Fatalf("typed-param s.run() not resolved; edges: %v", g.GetOutEdges(direct.ID))
	}
	if callEdgeTo(g, helper.ID, run.ID) == nil {
		t.Fatalf("this.worker.run() not resolved through field type; edges: %v", g.GetOutEdges(helper.ID))
	}
}

// `class C : B(), I` synthesizes the inheritance edges (extends the base
// class, implements the interface), and a call to an inherited base-class
// method resolves through the extends climb.
func TestKotlin_ExtendsImplementsAndInheritedCall(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"B.kt": `open class B {
    fun run() {}
}
`,
		"I.kt": `interface I {
    fun greet()
}
`,
		"C.kt": `class C : B(), I {
    override fun greet() {}

    fun go(c: C) {
        c.run()
    }
}
`,
	})
	p := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	c := nodeByNameKind(t, g, "C", graph.KindType)
	b := nodeByNameKind(t, g, "B", graph.KindType)
	iface := nodeByNameKind(t, g, "I", graph.KindInterface)

	ee := edgeBetween(g, c.ID, graph.EdgeExtends, b.ID)
	if ee == nil {
		t.Fatalf("extends edge C -> B missing; edges: %v", g.GetOutEdges(c.ID))
	}
	assertASTProvenance(t, ee, "kotlin-types")

	ie := edgeBetween(g, c.ID, graph.EdgeImplements, iface.ID)
	if ie == nil {
		t.Fatalf("implements edge C -> I missing; edges: %v", g.GetOutEdges(c.ID))
	}
	assertASTProvenance(t, ie, "kotlin-types")

	goMethod := nodeByNameKind(t, g, "go", graph.KindMethod)
	run := nodeByNameKind(t, g, "run", graph.KindMethod)
	if callEdgeTo(g, goMethod.ID, run.ID) == nil {
		t.Fatalf("inherited method call did not resolve through extends; edges: %v", g.GetOutEdges(goMethod.ID))
	}
}

// An ambiguous overload (two same-named methods, no way to choose) is
// skipped rather than guessed.
func TestKotlin_AmbiguousOverloadSkipped(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"K.kt": `class K {
    fun bar() {}
    fun bar(n: Int) {}
}
`,
		"App.kt": `class App {
    fun f(k: K) {
        k.bar()
    }
}
`,
	})
	p := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "f", graph.KindMethod)
	assertUntouched(t, g, caller.ID, "bar", "kotlin-types")
}

// A top-level extension function `fun Foo.ext()` declared in a different
// file from `class Foo` is callable as `foo.ext()` on any Foo receiver. The
// extractor's synthetic member_of edge points at a same-file phantom of the
// receiver type, so the call resolves through the extension fallback against
// the real cross-file Foo, at the direct AST band.
func TestKotlin_ExtensionFunctionResolvesAsMemberCall(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"Foo.kt": `class Foo {
}
`,
		"Ext.kt": `fun Foo.ext() {}
`,
		"App.kt": `class App {
    fun main(foo: Foo) {
        foo.ext()
    }
}
`,
	})
	p := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "main", graph.KindMethod)
	ext := nodeByNameKind(t, g, "ext", graph.KindMethod)
	e := callEdgeTo(g, caller.ID, ext.ID)
	if e == nil {
		t.Fatalf("extension call foo.ext() not resolved to %s; edges: %v", ext.ID, g.GetOutEdges(caller.ID))
	}
	assertASTProvenance(t, e, "kotlin-types")
}

// A nullable receiver `fun Foo?.ext2()` normalizes to receiver `Foo`, so
// `foo.ext2()` on a Foo receiver still resolves.
func TestKotlin_NullableReceiverExtensionResolves(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"Foo.kt": `class Foo {
}
`,
		"Ext.kt": `fun Foo?.ext2() {}
`,
		"App.kt": `class App {
    fun main(foo: Foo) {
        foo.ext2()
    }
}
`,
	})
	p := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "main", graph.KindMethod)
	ext := nodeByNameKind(t, g, "ext2", graph.KindMethod)
	if callEdgeTo(g, caller.ID, ext.ID) == nil {
		t.Fatalf("nullable-receiver extension foo.ext2() not resolved; edges: %v", g.GetOutEdges(caller.ID))
	}
}

// A real member shadows an extension of the same name (Kotlin semantics):
// `class Foo { fun m() }` plus `fun Foo.m()` resolves `foo.m()` to the REAL
// member, never the extension.
func TestKotlin_RealMemberShadowsExtension(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"Foo.kt": `class Foo {
    fun m() {}
}
`,
		"Ext.kt": `fun Foo.m() {}
`,
		"App.kt": `class App {
    fun main(foo: Foo) {
        foo.m()
    }
}
`,
	})
	p := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "main", graph.KindMethod)
	// The real member is the `m` whose owner is Foo; the extension `m` lives
	// in Ext.kt. Resolve the real member by its node ID convention.
	realMember := g.GetNode("Foo.kt::Foo.m")
	extMember := g.GetNode("Ext.kt::Foo.m")
	if realMember == nil || extMember == nil {
		t.Fatalf("expected both members in graph: real=%v ext=%v", realMember, extMember)
	}
	if callEdgeTo(g, caller.ID, realMember.ID) == nil {
		t.Fatalf("foo.m() did not resolve to the real member %s; edges: %v", realMember.ID, g.GetOutEdges(caller.ID))
	}
	if callEdgeTo(g, caller.ID, extMember.ID) != nil {
		t.Fatalf("foo.m() wrongly resolved to the extension %s over the real member", extMember.ID)
	}
}

// A plain top-level `fun free()` is not an extension: it carries no
// extension_receiver marker, so it never becomes a member of any type and a
// receiver-qualified `x.free()` does not resolve to it.
func TestKotlin_PlainTopLevelFunctionUnaffected(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"Foo.kt": `class Foo {
}
`,
		"Free.kt": `fun free() {}
`,
		"App.kt": `class App {
    fun main(foo: Foo) {
        foo.free()
    }
}
`,
	})
	free := nodeByNameKind(t, g, "free", graph.KindFunction)
	if nodeIsExtension(free) {
		t.Fatalf("plain top-level fun free() was marked as an extension: meta=%v", free.Meta)
	}
	p := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "main", graph.KindMethod)
	if callEdgeTo(g, caller.ID, free.ID) != nil {
		t.Fatalf("foo.free() wrongly resolved to the free function %s", free.ID)
	}
}

// EnrichFile resolves only the named file's calls, leaving others alone.
func TestKotlin_EnrichFileScopesToOneFile(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"Foo.kt": `class Foo {
    fun bar() {}
    fun baz() {}
}
`,
		"App.kt": `class App {
    fun main(x: Foo) {
        x.bar()
    }
}
`,
		"Other.kt": `class Other {
    fun go(x: Foo) {
        x.baz()
    }
}
`,
	})
	p := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := p.EnrichFile(g, dir, "App.kt"); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "main", graph.KindMethod)
	bar := nodeByNameKind(t, g, "bar", graph.KindMethod)
	if callEdgeTo(g, caller.ID, bar.ID) == nil {
		t.Fatalf("EnrichFile did not resolve the target file's call")
	}
	other := nodeByNameKind(t, g, "go", graph.KindMethod)
	assertUntouched(t, g, other.ID, "baz", "kotlin-types")
}
