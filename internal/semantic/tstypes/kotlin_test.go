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

// A binary `+` on a user type that declares `operator fun plus` desugars to
// the member call `a.plus(b)` and resolves to that operator function at the
// direct AST band.
func TestKotlin_OperatorPlusResolvesToMember(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"A.kt": `class A {
    operator fun plus(o: A): A {
        return this
    }
}
`,
		"App.kt": `class App {
    fun run(a: A, b: A) {
        val c = a + b
    }
}
`,
	})
	p := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "run", graph.KindMethod)
	plus := nodeByNameKind(t, g, "plus", graph.KindMethod)
	e := callEdgeTo(g, caller.ID, plus.ID)
	if e == nil {
		t.Fatalf("a + b did not desugar to A.plus; edges: %v", g.GetOutEdges(caller.ID))
	}
	assertASTProvenance(t, e, "kotlin-types")
}

// A subscript `a[i]` on a user type that declares `operator fun get` desugars
// to `a.get(i)` and resolves to that operator function.
func TestKotlin_OperatorGetResolvesToMember(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"A.kt": `class A {
    operator fun get(i: Int): A {
        return this
    }
}
`,
		"App.kt": `class App {
    fun run(a: A) {
        val c = a[0]
    }
}
`,
	})
	p := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "run", graph.KindMethod)
	get := nodeByNameKind(t, g, "get", graph.KindMethod)
	e := callEdgeTo(g, caller.ID, get.ID)
	if e == nil {
		t.Fatalf("a[0] did not desugar to A.get; edges: %v", g.GetOutEdges(caller.ID))
	}
	assertASTProvenance(t, e, "kotlin-types")
}

// A subscript assignment `a[i] = v` on a user type that declares
// `operator fun set` desugars to `a.set(i, v)` and resolves to it.
func TestKotlin_OperatorSetResolvesToMember(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"A.kt": `class A {
    operator fun set(i: Int, v: Int) {
    }
}
`,
		"App.kt": `class App {
    fun run(a: A) {
        a[0] = 5
    }
}
`,
	})
	p := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "run", graph.KindMethod)
	set := nodeByNameKind(t, g, "set", graph.KindMethod)
	if callEdgeTo(g, caller.ID, set.ID) == nil {
		t.Fatalf("a[0] = 5 did not desugar to A.set; edges: %v", g.GetOutEdges(caller.ID))
	}
}

// A membership test `x in coll` desugars to `coll.contains(x)` — the RECEIVER
// is the right-hand operand (the collection), so it resolves to the
// collection type's `operator fun contains`, not the element's.
func TestKotlin_OperatorInResolvesToContainsOnRHS(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"X.kt": `class X {
}
`,
		"Coll.kt": `class Coll {
    operator fun contains(x: X): Boolean {
        return true
    }
}
`,
		"App.kt": `class App {
    fun run(x: X, coll: Coll) {
        val r = x in coll
    }
}
`,
	})
	p := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "run", graph.KindMethod)
	contains := nodeByNameKind(t, g, "contains", graph.KindMethod)
	e := callEdgeTo(g, caller.ID, contains.ID)
	if e == nil {
		t.Fatalf("x in coll did not desugar to Coll.contains; edges: %v", g.GetOutEdges(caller.ID))
	}
	assertASTProvenance(t, e, "kotlin-types")
}

// A comparison `a < b` on a user type that declares `operator fun compareTo`
// desugars to `a.compareTo(b)` and resolves to it (all of < > <= >= map to
// compareTo).
func TestKotlin_OperatorComparisonResolvesToCompareTo(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"A.kt": `class A {
    operator fun compareTo(o: A): Int {
        return 0
    }
}
`,
		"App.kt": `class App {
    fun run(a: A, b: A) {
        val r = a < b
    }
}
`,
	})
	p := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "run", graph.KindMethod)
	cmp := nodeByNameKind(t, g, "compareTo", graph.KindMethod)
	e := callEdgeTo(g, caller.ID, cmp.ID)
	if e == nil {
		t.Fatalf("a < b did not desugar to A.compareTo; edges: %v", g.GetOutEdges(caller.ID))
	}
	assertASTProvenance(t, e, "kotlin-types")
}

// A for-loop `for (x in coll)` desugars to `coll.iterator()` and resolves to
// the collection type's `operator fun iterator`.
func TestKotlin_ForLoopResolvesToIterator(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"CollIter.kt": `class CollIter {
}
`,
		"Coll.kt": `class Coll {
    operator fun iterator(): CollIter {
        return CollIter()
    }
}
`,
		"App.kt": `class App {
    fun run(coll: Coll) {
        for (x in coll) {
        }
    }
}
`,
	})
	p := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "run", graph.KindMethod)
	iter := nodeByNameKind(t, g, "iterator", graph.KindMethod)
	if callEdgeTo(g, caller.ID, iter.ID) == nil {
		t.Fatalf("for (x in coll) did not desugar to Coll.iterator; edges: %v", g.GetOutEdges(caller.ID))
	}
}

// `1 + 2` is an operator on a primitive — there is no in-repo type with a
// `plus` member, so the desugaring resolves to nothing and emits NO edge, not
// even a spurious unresolved one.
func TestKotlin_PrimitiveOperatorEmitsNoEdge(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"App.kt": `class App {
    fun run() {
        val c = 1 + 2
    }
}
`,
	})
	p := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "run", graph.KindMethod)
	if edges := callEdgesNamed(g, caller.ID, "plus"); len(edges) != 0 {
		t.Fatalf("1 + 2 minted a plus edge; want none, got: %v", edges)
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

// An `is` smart-cast narrows a variable inside the then-branch:
// `if (x is Foo) { x.bar() }` resolves x.bar() to Foo::bar. The edge is
// graded inferred — it is derived from a guard, not a direct binding.
func TestKotlin_IsCheckThenBranchNarrows(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"Foo.kt": `class Foo {
    fun bar() {}
}
`,
		"App.kt": `fun f(x: Any) {
    if (x is Foo) {
        x.bar()
    }
}
`,
	})
	p := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "f", graph.KindFunction)
	target := nodeByNameKind(t, g, "bar", graph.KindMethod)
	e := callEdgeTo(g, caller.ID, target.ID)
	if e == nil {
		t.Fatalf("is-check then-branch narrowing did not resolve x.bar() to Foo::bar; edges: %v", g.GetOutEdges(caller.ID))
	}
	if e.Origin != graph.OriginASTResolved {
		t.Errorf("narrowed edge origin = %q, want %q", e.Origin, graph.OriginASTResolved)
	}
	if e.Meta["semantic_source"] != "kotlin-types" {
		t.Errorf("narrowed edge semantic_source = %v, want kotlin-types", e.Meta["semantic_source"])
	}
	if e.Meta["resolution_strategy"] != string(strategyInferred) {
		t.Errorf("narrowed edge resolution_strategy = %v, want %q", e.Meta["resolution_strategy"], strategyInferred)
	}
	if e.Confidence != inferredConfidence {
		t.Errorf("narrowed edge confidence = %v, want %v", e.Confidence, inferredConfidence)
	}
}

// A `!= null` check resolves a call on a nullable-typed receiver:
// `fun f(x: Foo?) { if (x != null) { x.bar() } }` resolves x.bar() to
// Foo::bar. In this engine the nullable annotation `Foo?` is already
// reduced to its non-null base `Foo` at bind time (normalizeKotlinType
// strips the `?`), so the receiver resolves on `Foo` through the existing
// param binding at the DIRECT band — the null check's only role is to not
// block that resolution.
func TestKotlin_NotNullThenBranchResolves(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"Foo.kt": `class Foo {
    fun bar() {}
}
`,
		"App.kt": `fun f(x: Foo?) {
    if (x != null) {
        x.bar()
    }
}
`,
	})
	p := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "f", graph.KindFunction)
	target := nodeByNameKind(t, g, "bar", graph.KindMethod)
	e := callEdgeTo(g, caller.ID, target.ID)
	if e == nil {
		t.Fatalf("null-check branch did not resolve x.bar() to Foo::bar; edges: %v", g.GetOutEdges(caller.ID))
	}
	assertASTProvenance(t, e, "kotlin-types")
}

// A negated `!is` guard with an early-exit body narrows the TAIL:
// `if (x !is Foo) return; x.bar()` resolves the trailing x.bar() to
// Foo::bar, because control past the guard implies x is Foo.
func TestKotlin_NotIsGuardTailNarrows(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"Foo.kt": `class Foo {
    fun bar() {}
}
`,
		"App.kt": `fun f(x: Any) {
    if (x !is Foo) return
    x.bar()
}
`,
	})
	p := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "f", graph.KindFunction)
	target := nodeByNameKind(t, g, "bar", graph.KindMethod)
	e := callEdgeTo(g, caller.ID, target.ID)
	if e == nil {
		t.Fatalf("guard-tail narrowing did not resolve x.bar() to Foo::bar; edges: %v", g.GetOutEdges(caller.ID))
	}
	if e.Meta["resolution_strategy"] != string(strategyInferred) {
		t.Errorf("guard-tail edge resolution_strategy = %v, want %q", e.Meta["resolution_strategy"], strategyInferred)
	}
	if e.Confidence != inferredConfidence {
		t.Errorf("guard-tail edge confidence = %v, want %v", e.Confidence, inferredConfidence)
	}
}

// The else branch is NOT narrowed: in
// `if (x is Foo) {} else { x.bar() }` the call in the else branch (where x
// is provably NOT Foo) must not resolve to Foo::bar.
func TestKotlin_IsCheckElseBranchNotNarrowed(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"Foo.kt": `class Foo {
    fun bar() {}
}
`,
		"App.kt": `fun f(x: Any) {
    if (x is Foo) {
    } else {
        x.bar()
    }
}
`,
	})
	p := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "f", graph.KindFunction)
	assertUntouched(t, g, caller.ID, "bar", "kotlin-types")
}

// A `when` type-match arm smart-casts the subject within that arm:
// `when (x) { is Foo -> x.bar() }` resolves x.bar() to Foo::bar at the
// inferred band.
func TestKotlin_WhenIsArmNarrows(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"Foo.kt": `class Foo {
    fun bar() {}
}
`,
		"App.kt": `fun f(x: Any) {
    when (x) {
        is Foo -> x.bar()
    }
}
`,
	})
	p := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "f", graph.KindFunction)
	target := nodeByNameKind(t, g, "bar", graph.KindMethod)
	e := callEdgeTo(g, caller.ID, target.ID)
	if e == nil {
		t.Fatalf("when is-arm narrowing did not resolve x.bar() to Foo::bar; edges: %v", g.GetOutEdges(caller.ID))
	}
	if e.Origin != graph.OriginASTResolved {
		t.Errorf("when-arm edge origin = %q, want %q", e.Origin, graph.OriginASTResolved)
	}
	if e.Meta["semantic_source"] != "kotlin-types" {
		t.Errorf("when-arm edge semantic_source = %v, want kotlin-types", e.Meta["semantic_source"])
	}
	if e.Meta["resolution_strategy"] != string(strategyInferred) {
		t.Errorf("when-arm edge resolution_strategy = %v, want %q", e.Meta["resolution_strategy"], strategyInferred)
	}
	if e.Confidence != inferredConfidence {
		t.Errorf("when-arm edge confidence = %v, want %v", e.Confidence, inferredConfidence)
	}
}

// A Kotlin `data class` auto-generates one `componentN()` accessor per
// primary-constructor property (in order) and a `copy()` returning the
// class. The extractor synthesizes these as members, so `p.componentN()` /
// `p.copy()` resolve through the engine's member lookup with no engine
// change. componentI's declared return type is the I-th property's type;
// copy's is the class itself.
func TestKotlin_DataClassComponentAndCopyResolve(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"types.kt": `class A { fun ma() {} }
class B { fun mb() {} }
`,
		"P.kt": `data class P(val a: A, val b: B)
`,
		"App.kt": `class App {
    fun run() {
        val p = P(A(), B())
        p.component1()
        p.component2()
        p.copy()
    }
}
`,
	})
	prov := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := prov.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "run", graph.KindMethod)
	for _, tc := range []struct{ method, wantReturn string }{
		{"component1", "A"},
		{"component2", "B"},
		{"copy", "P"},
	} {
		targetID := "P.kt::P." + tc.method
		target := g.GetNode(targetID)
		if target == nil {
			t.Fatalf("synthetic member %q not emitted", targetID)
		}
		if target.Meta["synthetic"] != "data_class" {
			t.Errorf("%s Meta[synthetic] = %v, want data_class", tc.method, target.Meta["synthetic"])
		}
		if target.Meta["generated"] != true {
			t.Errorf("%s Meta[generated] = %v, want true", tc.method, target.Meta["generated"])
		}
		if target.Meta["return_type"] != tc.wantReturn {
			t.Errorf("%s Meta[return_type] = %v, want %q", tc.method, target.Meta["return_type"], tc.wantReturn)
		}
		e := callEdgeTo(g, caller.ID, targetID)
		if e == nil {
			t.Fatalf("p.%s() not resolved to %q; edges: %v", tc.method, targetID, g.GetOutEdges(caller.ID))
		}
		assertASTProvenance(t, e, "kotlin-types")
	}
}

// `copy()` returns the data class, so a `p.copy().componentN()` chain types
// its receiver from copy's return type and resolves the trailing call — at
// the graded inferred band (the receiver came through a return-type rewrite).
func TestKotlin_DataClassCopyChainResolves(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"types.kt": `class A { fun ma() {} }
`,
		"P.kt": `data class P(val a: A)
`,
		"App.kt": `class App {
    fun run() {
        val p = P(A())
        p.copy().component1()
    }
}
`,
	})
	prov := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := prov.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "run", graph.KindMethod)
	target := g.GetNode("P.kt::P.component1")
	if target == nil {
		t.Fatal("synthetic component1 not emitted")
	}
	e := callEdgeTo(g, caller.ID, target.ID)
	if e == nil {
		t.Fatalf("p.copy().component1() not resolved; edges: %v", g.GetOutEdges(caller.ID))
	}
	if e.Origin != graph.OriginASTResolved {
		t.Errorf("chain edge origin = %q, want %q", e.Origin, graph.OriginASTResolved)
	}
	if e.Meta["semantic_source"] != "kotlin-types" {
		t.Errorf("chain edge semantic_source = %v, want kotlin-types", e.Meta["semantic_source"])
	}
}

// A plain (non-`data`) class gets NO componentN / copy synthesis, so a
// `q.component1()` call stays unresolved and untouched.
func TestKotlin_NonDataClassNoComponentSynthesis(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"types.kt": `class A { fun ma() {} }
`,
		"Q.kt": `class Q(val a: A)
`,
		"App.kt": `class App {
    fun run() {
        val q = Q(A())
        q.component1()
    }
}
`,
	})
	prov := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := prov.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	if n := g.GetNode("Q.kt::Q.component1"); n != nil {
		t.Fatalf("non-data class Q must not synthesize component1, got %v", n)
	}
	caller := nodeByNameKind(t, g, "run", graph.KindMethod)
	assertUntouched(t, g, caller.ID, "component1", "kotlin-types")
}

// A user-declared `copy` wins over the synthetic one: synthesis is skipped
// for that name (emitting both would make the member lookup ambiguous), so
// `r.copy()` resolves to the user's method, and componentN is still
// synthesized for the properties the user did not redeclare.
func TestKotlin_DataClassUserCopyWins(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"types.kt": `class A { fun ma() {} }
`,
		"R.kt": `data class R(val a: A) {
    fun copy(): R = this
}
`,
		"App.kt": `class App {
    fun run() {
        val r = R(A())
        r.copy()
    }
}
`,
	})
	prov := NewProvider(KotlinSpec(), zap.NewNop())
	if _, err := prov.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	copies := 0
	for _, n := range g.FindNodesByName("copy") {
		if n.Kind != graph.KindMethod {
			continue
		}
		copies++
		if n.Meta["synthetic"] == "data_class" {
			t.Errorf("synthetic copy emitted despite user-declared copy: %s", n.ID)
		}
	}
	if copies != 1 {
		t.Fatalf("want exactly 1 copy method, got %d", copies)
	}
	caller := nodeByNameKind(t, g, "run", graph.KindMethod)
	target := nodeByNameKind(t, g, "copy", graph.KindMethod)
	if e := callEdgeTo(g, caller.ID, target.ID); e == nil {
		t.Fatalf("r.copy() did not resolve to user copy; edges: %v", g.GetOutEdges(caller.ID))
	}
	if g.GetNode("R.kt::R.component1") == nil {
		t.Error("component1 should still be synthesized when only copy is user-declared")
	}
}
