package tstypes

import (
	"testing"

	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/graph"
)

// A typed parameter grounds its receiver: `$x->bar()` on a `Foo $x`
// resolves to Foo::bar.
func TestPHP_TypedParamResolvesCall(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app.php": `<?php
class Foo {
    public function bar(): void {}
}

class App {
    public function f(Foo $x): void {
        $x->bar();
    }
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	res, err := p.Enrich(g, dir)
	if err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "f", graph.KindMethod)
	target := nodeByNameKind(t, g, "bar", graph.KindMethod)
	e := callEdgeTo(g, caller.ID, target.ID)
	if e == nil {
		t.Fatalf("typed-param call %s -> %s not resolved; edges: %v", caller.ID, target.ID, g.GetOutEdges(caller.ID))
	}
	assertASTProvenance(t, e, "php-types")
	if res.EdgesConfirmed+res.EdgesAdded == 0 {
		t.Errorf("result reported no edge work: %+v", res)
	}
}

// `$this->field->method()` resolves through the declared property type.
func TestPHP_ThisFieldResolvesCall(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app.php": `<?php
class Foo {
    public function bar(): void {}
}

class App {
    private Foo $x;

    public function f(): void {
        $this->x->bar();
    }
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "f", graph.KindMethod)
	target := nodeByNameKind(t, g, "bar", graph.KindMethod)
	if callEdgeTo(g, caller.ID, target.ID) == nil {
		t.Fatalf("$this->x->bar() not resolved through field type; edges: %v", g.GetOutEdges(caller.ID))
	}
}

// `(new Foo())->bar()` and the parenthesis-free `new Foo()->bar()` both
// type their receiver from the constructor expression.
func TestPHP_NewExprChainResolvesCall(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app.php": `<?php
class Foo {
    public function bar(): void {}
}

class App {
    public function f(): void {
        (new Foo())->bar();
        new Foo()->bar();
    }
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "f", graph.KindMethod)
	target := nodeByNameKind(t, g, "bar", graph.KindMethod)
	e := callEdgeTo(g, caller.ID, target.ID)
	if e == nil {
		t.Fatalf("new-expression chain not resolved; edges: %v", g.GetOutEdges(caller.ID))
	}
	assertASTProvenance(t, e, "php-types")
}

// A local bound from `new Foo()` propagates its type to a later call.
func TestPHP_LocalConstructorInferenceResolvesCall(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app.php": `<?php
class Foo {
    public function bar(): void {}
}

class App {
    public function f(): void {
        $o = new Foo();
        $o->bar();
    }
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "f", graph.KindMethod)
	target := nodeByNameKind(t, g, "bar", graph.KindMethod)
	if callEdgeTo(g, caller.ID, target.ID) == nil {
		t.Fatalf("constructor-inferred local call not resolved; edges: %v", g.GetOutEdges(caller.ID))
	}
}

// A static `Foo::make()` resolves to the named class's method.
func TestPHP_StaticCallResolves(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app.php": `<?php
class Foo {
    public static function make(): void {}
}

class App {
    public function f(): void {
        Foo::make();
    }
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "f", graph.KindMethod)
	target := nodeByNameKind(t, g, "make", graph.KindMethod)
	e := callEdgeTo(g, caller.ID, target.ID)
	if e == nil {
		t.Fatalf("static Foo::make() not resolved; edges: %v", g.GetOutEdges(caller.ID))
	}
	assertASTProvenance(t, e, "php-types")
}

// A constructor-promoted property is treated as a typed field, so a
// call through it resolves.
func TestPHP_PromotedParamFieldResolvesCall(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app.php": `<?php
class Dep {
    public function work(): void {}
}

class App {
    public function __construct(private Dep $dep) {}

    public function f(): void {
        $this->dep->work();
    }
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "f", graph.KindMethod)
	target := nodeByNameKind(t, g, "work", graph.KindMethod)
	if callEdgeTo(g, caller.ID, target.ID) == nil {
		t.Fatalf("promoted-property call not resolved; edges: %v", g.GetOutEdges(caller.ID))
	}
}

// Assigning a typed parameter to a property gives the property that
// type, even when the property declaration itself is untyped.
func TestPHP_ThisFieldFromParamInference(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app.php": `<?php
class Dep {
    public function work(): void {}
}

class App {
    private $cached;

    public function __construct(Dep $seed) {
        $this->cached = $seed;
    }

    public function f(): void {
        $this->cached->work();
    }
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "f", graph.KindMethod)
	target := nodeByNameKind(t, g, "work", graph.KindMethod)
	if callEdgeTo(g, caller.ID, target.ID) == nil {
		t.Fatalf("property-from-parameter inference did not resolve the call; edges: %v", g.GetOutEdges(caller.ID))
	}
}

// `class Impl extends Base implements Greeter` synthesizes the
// inheritance edges, and a call to an inherited method resolves through
// the extends climb.
func TestPHP_ExtendsImplementsAndInheritedCall(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app.php": `<?php
interface Greeter {
    public function greet(): void;
}

class Base {
    public function run(): void {}
}

class Impl extends Base implements Greeter {
    public function greet(): void {}

    public function go(Impl $i): void {
        $i->run();
    }
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	impl := nodeByNameKind(t, g, "Impl", graph.KindType)
	base := nodeByNameKind(t, g, "Base", graph.KindType)
	iface := nodeByNameKind(t, g, "Greeter", graph.KindInterface)

	ee := edgeBetween(g, impl.ID, graph.EdgeExtends, base.ID)
	if ee == nil {
		t.Fatalf("extends edge missing; edges: %v", g.GetOutEdges(impl.ID))
	}
	assertASTProvenance(t, ee, "php-types")

	ie := edgeBetween(g, impl.ID, graph.EdgeImplements, iface.ID)
	if ie == nil {
		t.Fatalf("implements edge missing; edges: %v", g.GetOutEdges(impl.ID))
	}
	assertASTProvenance(t, ie, "php-types")

	goMethod := nodeByNameKind(t, g, "go", graph.KindMethod)
	run := nodeByNameKind(t, g, "run", graph.KindMethod)
	if callEdgeTo(g, goMethod.ID, run.ID) == nil {
		t.Fatalf("inherited method call did not resolve through extends; edges: %v", g.GetOutEdges(goMethod.ID))
	}
}

// An ambiguous overload (two same-named methods, no way to choose) is
// skipped rather than guessed.
func TestPHP_AmbiguousOverloadSkipped(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app.php": `<?php
class K {
    public function bar() {}
    public function bar() {}
}

class App {
    public function f(K $k): void {
        $k->bar();
    }
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "f", graph.KindMethod)
	assertUntouched(t, g, caller.ID, "bar", "php-types")
}

// `use App\Service` binds the short name to the imported FQN, steering a
// cross-file resolution onto the right package when several types share
// a name.
func TestPHP_ImportHintDisambiguatesCrossFile(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"App/Service.php": `<?php
namespace App;

class Service {
    public function run(): void {}
}
`,
		"Other/Service.php": `<?php
namespace Other;

class Service {
    public function run(): void {}
}
`,
		"Client/Handler.php": `<?php
namespace Client;

use App\Service;

class Handler {
    public function handle(Service $s): void {
        $s->run();
    }
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "handle", graph.KindMethod)
	want := "App/Service.php::Service.run"
	if callEdgeTo(g, caller.ID, want) == nil {
		t.Fatalf("import-hinted call did not land on %s; edges: %v", want, g.GetOutEdges(caller.ID))
	}
	wrong := "Other/Service.php::Service.run"
	if callEdgeTo(g, caller.ID, wrong) != nil {
		t.Fatalf("call landed on the wrong namespace's type %s", wrong)
	}
}

// A trait method called on a using-class receiver resolves through the
// trait-composition (`use T;`) edge: `$c->fn()` lands on T::fn.
func TestPHP_TraitMethodResolvesCall(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app.php": `<?php
trait T {
    public function fn(): void {}
}

class C {
    use T;
}

class App {
    public function run(C $c): void {
        $c->fn();
    }
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	c := nodeByNameKind(t, g, "C", graph.KindType)
	traitT := nodeByNameKind(t, g, "T", graph.KindType)
	// The unresolved `use T;` extends edge is resolved onto the trait node.
	if edgeBetween(g, c.ID, graph.EdgeExtends, traitT.ID) == nil {
		t.Fatalf("trait-use extends edge C -> T not resolved; edges: %v", g.GetOutEdges(c.ID))
	}
	run := nodeByNameKind(t, g, "run", graph.KindMethod)
	fn := nodeByNameKind(t, g, "fn", graph.KindMethod)
	e := callEdgeTo(g, run.ID, fn.ID)
	if e == nil {
		t.Fatalf("trait-method call $c->fn() not resolved to T::fn; edges: %v", g.GetOutEdges(run.ID))
	}
	assertASTProvenance(t, e, "php-types")
}

// A fluent trait method returning the trait type (`: self`) rebinds to
// the using class when chained: `$c->step()->done()` types step()'s
// result as C, so done() resolves on C. The inner trait call is direct;
// the outer chained edge is graded inferred.
func TestPHP_TraitSelfReturnChainRebindsToUsingClass(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app.php": `<?php
trait T {
    public function step(): self { return $this; }
}

class C {
    use T;

    public function done(): void {}
}

class App {
    public function run(C $c): void {
        $c->step()->done();
    }
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	run := nodeByNameKind(t, g, "run", graph.KindMethod)
	step := nodeByNameKind(t, g, "step", graph.KindMethod)
	done := nodeByNameKind(t, g, "done", graph.KindMethod)

	inner := callEdgeTo(g, run.ID, step.ID)
	if inner == nil {
		t.Fatalf("inner trait call $c->step() not resolved to T::step; edges: %v", g.GetOutEdges(run.ID))
	}
	assertASTProvenance(t, inner, "php-types")
	if inner.Meta["resolution_strategy"] == string(strategyInferred) {
		t.Errorf("inner direct trait call should not be graded inferred")
	}

	outer := callEdgeTo(g, run.ID, done.ID)
	if outer == nil {
		t.Fatalf("chained $c->step()->done() not rebound to C::done; edges: %v", g.GetOutEdges(run.ID))
	}
	if outer.Origin != graph.OriginASTResolved {
		t.Errorf("chained edge origin = %q, want %q", outer.Origin, graph.OriginASTResolved)
	}
	if outer.Meta["semantic_source"] != "php-types" {
		t.Errorf("chained edge semantic_source = %v, want php-types", outer.Meta["semantic_source"])
	}
	if outer.Meta["resolution_strategy"] != string(strategyInferred) {
		t.Errorf("chained edge resolution_strategy = %v, want %q", outer.Meta["resolution_strategy"], strategyInferred)
	}
	if outer.Confidence != inferredConfidence {
		t.Errorf("chained edge confidence = %v, want %v", outer.Confidence, inferredConfidence)
	}
}

// A fluent trait method declaring its own trait name as the return type
// (`: T`) also rebinds to the using class when chained.
func TestPHP_TraitNamedReturnChainRebindsToUsingClass(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app.php": `<?php
trait T {
    public function step(): T { return $this; }
}

class C {
    use T;

    public function done(): void {}
}

class App {
    public function run(C $c): void {
        $c->step()->done();
    }
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	run := nodeByNameKind(t, g, "run", graph.KindMethod)
	done := nodeByNameKind(t, g, "done", graph.KindMethod)
	outer := callEdgeTo(g, run.ID, done.ID)
	if outer == nil {
		t.Fatalf("chained $c->step()->done() (`: T` return) not rebound to C::done; edges: %v", g.GetOutEdges(run.ID))
	}
	if outer.Meta["resolution_strategy"] != string(strategyInferred) {
		t.Errorf("chained edge resolution_strategy = %v, want %q", outer.Meta["resolution_strategy"], strategyInferred)
	}
}

// A trait-use alias (`use T { T::fn as renamed; }`) exposes the renamed
// member on the using class: `$c->renamed()` resolves to T::fn.
func TestPHP_TraitAliasResolvesToOriginalMethod(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app.php": `<?php
trait T {
    public function fn(): void {}
}

class C {
    use T {
        T::fn as renamed;
    }
}

class App {
    public function run(C $c): void {
        $c->renamed();
    }
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	run := nodeByNameKind(t, g, "run", graph.KindMethod)
	fn := nodeByNameKind(t, g, "fn", graph.KindMethod)
	e := callEdgeTo(g, run.ID, fn.ID)
	if e == nil {
		t.Fatalf("aliased call $c->renamed() not resolved to T::fn; edges: %v", g.GetOutEdges(run.ID))
	}
	assertASTProvenance(t, e, "php-types")
}

// A trait conflict resolved with `insteadof` is NOT precedence-resolved:
// the member stays ambiguous across the two traits, so the call is
// skipped rather than bound to one arbitrary side, and nothing crashes.
func TestPHP_TraitInsteadofIsSkippedNotMisresolved(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app.php": `<?php
trait A {
    public function fn(): void {}
}

trait B {
    public function fn(): void {}
}

class C {
    use A, B {
        A::fn insteadof B;
    }
}

class App {
    public function run(C $c): void {
        $c->fn();
    }
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	run := nodeByNameKind(t, g, "run", graph.KindMethod)
	// Ambiguous across A::fn and B::fn → no engine-resolved edge for fn.
	assertUntouched(t, g, run.ID, "fn", "php-types")
}

// An `instanceof` guard narrows a variable inside the then-branch:
// `if ($x instanceof Foo) { $x->bar(); }` resolves $x->bar() to Foo::bar.
// The edge is graded inferred — it is derived from a guard, not a direct
// binding.
func TestPHP_InstanceofThenBranchNarrows(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app.php": `<?php
class Foo {
    public function bar(): void {}
}

function f($x): void {
    if ($x instanceof Foo) {
        $x->bar();
    }
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "f", graph.KindFunction)
	target := nodeByNameKind(t, g, "bar", graph.KindMethod)
	e := callEdgeTo(g, caller.ID, target.ID)
	if e == nil {
		t.Fatalf("instanceof then-branch narrowing did not resolve $x->bar() to Foo::bar; edges: %v", g.GetOutEdges(caller.ID))
	}
	if e.Origin != graph.OriginASTResolved {
		t.Errorf("narrowed edge origin = %q, want %q", e.Origin, graph.OriginASTResolved)
	}
	if e.Meta["semantic_source"] != "php-types" {
		t.Errorf("narrowed edge semantic_source = %v, want php-types", e.Meta["semantic_source"])
	}
	if e.Meta["resolution_strategy"] != string(strategyInferred) {
		t.Errorf("narrowed edge resolution_strategy = %v, want %q", e.Meta["resolution_strategy"], strategyInferred)
	}
	if e.Confidence != inferredConfidence {
		t.Errorf("narrowed edge confidence = %v, want %v", e.Confidence, inferredConfidence)
	}
}

// A negated `instanceof` guard with an early-exit body narrows the TAIL:
// `if (!($x instanceof Foo)) { return; } $x->bar();` resolves the trailing
// $x->bar() to Foo::bar, because control past the guard implies $x is Foo.
func TestPHP_InstanceofGuardTailNarrows(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app.php": `<?php
class Foo {
    public function bar(): void {}
}

function f($x): void {
    if (!($x instanceof Foo)) {
        return;
    }
    $x->bar();
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "f", graph.KindFunction)
	target := nodeByNameKind(t, g, "bar", graph.KindMethod)
	e := callEdgeTo(g, caller.ID, target.ID)
	if e == nil {
		t.Fatalf("guard-tail narrowing did not resolve $x->bar() to Foo::bar; edges: %v", g.GetOutEdges(caller.ID))
	}
	if e.Meta["resolution_strategy"] != string(strategyInferred) {
		t.Errorf("guard-tail edge resolution_strategy = %v, want %q", e.Meta["resolution_strategy"], strategyInferred)
	}
	if e.Confidence != inferredConfidence {
		t.Errorf("guard-tail edge confidence = %v, want %v", e.Confidence, inferredConfidence)
	}
}

// The else branch is NOT narrowed: in
// `if ($x instanceof Foo) {} else { $x->bar(); }` the call in the else
// branch (where $x is provably NOT Foo) must not resolve to Foo::bar.
func TestPHP_InstanceofElseBranchNotNarrowed(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app.php": `<?php
class Foo {
    public function bar(): void {}
}

function f($x): void {
    if ($x instanceof Foo) {
    } else {
        $x->bar();
    }
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "f", graph.KindFunction)
	assertUntouched(t, g, caller.ID, "bar", "php-types")
}

// A non-guard negated instanceof (the if does NOT early-exit) narrows
// nothing in the tail: `if (!($x instanceof Foo)) { $x->bar(); }`
// resolves in the then-branch only by the NEGATED sense, which v1 does
// not narrow — so neither the then-branch nor the tail binds Foo. Guards
// the conservativeness contract: only an early-exit body narrows the tail.
func TestPHP_NegatedInstanceofWithoutEarlyExitDoesNotNarrowTail(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app.php": `<?php
class Foo {
    public function bar(): void {}
}

function f($x): void {
    if (!($x instanceof Foo)) {
        $x->bar();
    }
    $x->bar();
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "f", graph.KindFunction)
	assertUntouched(t, g, caller.ID, "bar", "php-types")
}

// A `/** @var Foo $x */` docblock on a local assignment types the local
// even when the initializer is an unresolvable call: `$x->bar()` resolves
// to Foo::bar. The edge is graded inferred — a comment, not a checked
// annotation.
func TestPHP_DocVarLocalResolvesCall(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app.php": `<?php
class Foo {
    public function bar(): void {}
}

function f(): void {
    /** @var Foo $x */
    $x = makeIt();
    $x->bar();
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "f", graph.KindFunction)
	target := nodeByNameKind(t, g, "bar", graph.KindMethod)
	e := callEdgeTo(g, caller.ID, target.ID)
	if e == nil {
		t.Fatalf("@var local did not resolve $x->bar() to Foo::bar; edges: %v", g.GetOutEdges(caller.ID))
	}
	if e.Origin != graph.OriginASTResolved {
		t.Errorf("@var edge origin = %q, want %q", e.Origin, graph.OriginASTResolved)
	}
	if e.Meta["semantic_source"] != "php-types" {
		t.Errorf("@var edge semantic_source = %v, want php-types", e.Meta["semantic_source"])
	}
	if e.Meta["resolution_strategy"] != string(strategyInferred) {
		t.Errorf("@var edge resolution_strategy = %v, want %q", e.Meta["resolution_strategy"], strategyInferred)
	}
	if e.Confidence != inferredConfidence {
		t.Errorf("@var edge confidence = %v, want %v", e.Confidence, inferredConfidence)
	}
}

// A `/** @return $this */` docblock on a method makes the method return the
// enclosing class, so a fluent chain `$c->chain()->other()` resolves
// other() on that class. The chained edge is graded inferred.
func TestPHP_DocReturnThisChainResolves(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app.php": `<?php
class C {
    /** @return $this */
    public function chain() { return $this; }

    public function other(): void {}
}

class App {
    public function run(C $c): void {
        $c->chain()->other();
    }
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	run := nodeByNameKind(t, g, "run", graph.KindMethod)
	other := nodeByNameKind(t, g, "other", graph.KindMethod)
	outer := callEdgeTo(g, run.ID, other.ID)
	if outer == nil {
		t.Fatalf("@return $this chain did not resolve $c->chain()->other() to C::other; edges: %v", g.GetOutEdges(run.ID))
	}
	if outer.Meta["resolution_strategy"] != string(strategyInferred) {
		t.Errorf("chained edge resolution_strategy = %v, want %q", outer.Meta["resolution_strategy"], strategyInferred)
	}
	if outer.Confidence != inferredConfidence {
		t.Errorf("chained edge confidence = %v, want %v", outer.Confidence, inferredConfidence)
	}
}

// A `/** @param Foo $x */` docblock types a parameter that has NO native
// annotation, so `$x->bar()` inside resolves to Foo::bar at the inferred
// band.
func TestPHP_DocParamWithoutNativeResolvesCall(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app.php": `<?php
class Foo {
    public function bar(): void {}
}

/** @param Foo $x */
function withParam($x): void {
    $x->bar();
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "withParam", graph.KindFunction)
	target := nodeByNameKind(t, g, "bar", graph.KindMethod)
	e := callEdgeTo(g, caller.ID, target.ID)
	if e == nil {
		t.Fatalf("@param without native type did not resolve $x->bar() to Foo::bar; edges: %v", g.GetOutEdges(caller.ID))
	}
	if e.Meta["resolution_strategy"] != string(strategyInferred) {
		t.Errorf("@param edge resolution_strategy = %v, want %q", e.Meta["resolution_strategy"], strategyInferred)
	}
	if e.Confidence != inferredConfidence {
		t.Errorf("@param edge confidence = %v, want %v", e.Confidence, inferredConfidence)
	}
}

// A union return `/** @return Foo|null */` resolves to the LEFTMOST
// NON-NULL member (Foo): the fluent chain `$c->make()->bar()` resolves
// bar() on Foo.
func TestPHP_DocReturnUnionLeftmostNonNull(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app.php": `<?php
class Foo {
    public function bar(): void {}
}

class C {
    /** @return Foo|null */
    public function make() { return null; }
}

class App {
    public function run(C $c): void {
        $c->make()->bar();
    }
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	run := nodeByNameKind(t, g, "run", graph.KindMethod)
	bar := nodeByNameKind(t, g, "bar", graph.KindMethod)
	if callEdgeTo(g, run.ID, bar.ID) == nil {
		t.Fatalf("@return Foo|null did not resolve the chain to Foo::bar; edges: %v", g.GetOutEdges(run.ID))
	}
}

// A union `/** @var A|B $x */` types the local as the LEFTMOST NON-NULL
// member (A): `$x->m()` resolves to A::m, never B::m.
func TestPHP_DocVarUnionLeftmostNonNull(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app.php": `<?php
class A {
    public function m(): void {}
}
class B {
    public function m(): void {}
}

function f(): void {
    /** @var A|B $x */
    $x = makeIt();
    $x->m();
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "f", graph.KindFunction)
	wantA := "app.php::A.m"
	if callEdgeTo(g, caller.ID, wantA) == nil {
		t.Fatalf("@var A|B did not resolve to A::m (leftmost non-null); edges: %v", g.GetOutEdges(caller.ID))
	}
	wrongB := "app.php::B.m"
	if callEdgeTo(g, caller.ID, wrongB) != nil {
		t.Fatalf("@var A|B wrongly resolved to B::m")
	}
}

// A native parameter annotation ALWAYS wins over a conflicting `@param`
// docblock: `Bar $x` with `/** @param Foo $x */` resolves `$x->m()` to
// Bar::m, not Foo::m, and at the direct (not inferred) band.
func TestPHP_DocParamNativeAnnotationWins(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app.php": `<?php
class Foo {
    public function m(): void {}
}
class Bar {
    public function m(): void {}
}

/** @param Foo $x */
function withBoth(Bar $x): void {
    $x->m();
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "withBoth", graph.KindFunction)
	wantBar := "app.php::Bar.m"
	e := callEdgeTo(g, caller.ID, wantBar)
	if e == nil {
		t.Fatalf("native Bar $x did not win over @param Foo; edges: %v", g.GetOutEdges(caller.ID))
	}
	wrongFoo := "app.php::Foo.m"
	if callEdgeTo(g, caller.ID, wrongFoo) != nil {
		t.Fatalf("docblock @param Foo wrongly overrode native Bar")
	}
	if e.Meta["resolution_strategy"] == string(strategyInferred) {
		t.Errorf("native-annotation edge should be direct, got inferred")
	}
	if e.Confidence < astConfidence {
		t.Errorf("native-annotation edge confidence = %v, want >= %v", e.Confidence, astConfidence)
	}
}

// An untyped property with a `/** @var Dep */` docblock types the field, so
// `$this->cached->work()` resolves to Dep::work at the inferred band.
func TestPHP_DocVarPropertyResolvesCall(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app.php": `<?php
class Dep {
    public function work(): void {}
}

class App {
    /** @var Dep */
    private $cached;

    public function f(): void {
        $this->cached->work();
    }
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "f", graph.KindMethod)
	target := nodeByNameKind(t, g, "work", graph.KindMethod)
	e := callEdgeTo(g, caller.ID, target.ID)
	if e == nil {
		t.Fatalf("@var property did not resolve $this->cached->work() to Dep::work; edges: %v", g.GetOutEdges(caller.ID))
	}
	if e.Meta["resolution_strategy"] != string(strategyInferred) {
		t.Errorf("@var property edge resolution_strategy = %v, want %q", e.Meta["resolution_strategy"], strategyInferred)
	}
	if e.Confidence != inferredConfidence {
		t.Errorf("@var property edge confidence = %v, want %v", e.Confidence, inferredConfidence)
	}
}

// EnrichFile resolves only the named file's calls, leaving others alone.
func TestPHP_EnrichFileScopesToOneFile(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"foo.php": `<?php
class Foo {
    public function bar(): void {}
    public function baz(): void {}
}
`,
		"app.php": `<?php
class App {
    public function main(Foo $x): void {
        $x->bar();
    }
}
`,
		"other.php": `<?php
class Other {
    public function go(Foo $x): void {
        $x->baz();
    }
}
`,
	})
	p := NewProvider(PHPSpec(), zap.NewNop())
	if _, err := p.EnrichFile(g, dir, "app.php"); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "main", graph.KindMethod)
	bar := nodeByNameKind(t, g, "bar", graph.KindMethod)
	if callEdgeTo(g, caller.ID, bar.ID) == nil {
		t.Fatalf("EnrichFile did not resolve the target file's call")
	}
	other := nodeByNameKind(t, g, "go", graph.KindMethod)
	assertUntouched(t, g, other.ID, "baz", "php-types")
}
