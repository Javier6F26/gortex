package tstypes

import (
	"testing"

	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/graph"
)

const rustSvc = `pub struct Svc {
    count: u32,
}

impl Svc {
    pub fn run(&self) {}
    pub fn stop(&self) {}
}
`

func TestRust_DeclaredParamTypeResolvesCall(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"src/engine.rs": rustSvc,
		"src/app.rs": `use crate::engine::Svc;

pub fn handle(s: &Svc) {
    s.run();
}
`,
	})
	p := NewProvider(RustSpec(), zap.NewNop())
	res, err := p.Enrich(g, dir)
	if err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "handle", graph.KindFunction)
	target := nodeByNameKind(t, g, "run", graph.KindMethod)
	e := callEdgeTo(g, caller.ID, target.ID)
	if e == nil {
		t.Fatalf("annotated-param call not resolved; edges: %v", g.GetOutEdges(caller.ID))
	}
	assertASTProvenance(t, e, "rust-types")
	if res.EdgesConfirmed+res.EdgesAdded == 0 {
		t.Errorf("result reported no edge work: %+v", res)
	}
}

// Both constructor shapes ground a receiver: the struct expression and
// the `T::new()` convention.
func TestRust_ConstructorInferenceResolvesCall(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"src/engine.rs": rustSvc,
		"src/app.rs": `use crate::engine::Svc;

pub fn literal() {
    let s = Svc { count: 0 };
    s.run();
}

pub fn convention() {
    let s = Svc::new();
    s.stop();
}
`,
	})
	p := NewProvider(RustSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	literal := nodeByNameKind(t, g, "literal", graph.KindFunction)
	run := nodeByNameKind(t, g, "run", graph.KindMethod)
	if callEdgeTo(g, literal.ID, run.ID) == nil {
		t.Fatalf("struct-expression call not resolved; edges: %v", g.GetOutEdges(literal.ID))
	}
	convention := nodeByNameKind(t, g, "convention", graph.KindFunction)
	stop := nodeByNameKind(t, g, "stop", graph.KindMethod)
	if callEdgeTo(g, convention.ID, stop.ID) == nil {
		t.Fatalf("Svc::new() call not resolved; edges: %v", g.GetOutEdges(convention.ID))
	}
}

// Two same-named structs: the use-path hint picks the matching module
// file.
func TestRust_ImportHintDisambiguates(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"src/engine.rs": rustSvc,
		"src/legacy.rs": rustSvc,
		"src/app.rs": `use crate::engine::Svc;

pub fn main() {
    let s = Svc::new();
    s.run();
}
`,
	})
	p := NewProvider(RustSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "main", graph.KindFunction)
	want := "src/engine.rs::Svc.run"
	if callEdgeTo(g, caller.ID, want) == nil {
		t.Fatalf("use-hinted call did not land on %s; edges: %v", want, g.GetOutEdges(caller.ID))
	}
	if callEdgeTo(g, caller.ID, "src/legacy.rs::Svc.run") != nil {
		t.Fatal("call landed on the wrong module's struct")
	}
}

func TestRust_ImplTraitImplementsSynthesis(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"src/greeter.rs": `pub trait Greeter {
    fn greet(&self);
}
`,
		"src/widget.rs": `use crate::greeter::Greeter;

pub struct Widget {
    id: u32,
}

impl Greeter for Widget {
    fn greet(&self) {}
}
`,
	})
	p := NewProvider(RustSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	widget := nodeByNameKind(t, g, "Widget", graph.KindType)
	trait := nodeByNameKind(t, g, "Greeter", graph.KindInterface)
	e := edgeBetween(g, widget.ID, graph.EdgeImplements, trait.ID)
	if e == nil {
		t.Fatalf("implements edge missing; edges: %v", g.GetOutEdges(widget.ID))
	}
	assertASTProvenance(t, e, "rust-types")
}

// self-qualified calls and struct-field receivers resolve through the
// impl block, with the field declared on the struct.
func TestRust_SelfAndFieldReceivers(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"src/engine.rs": rustSvc,
		"src/app.rs": `use crate::engine::Svc;

pub struct App {
    worker: Svc,
}

impl App {
    pub fn direct(&self) {
        self.helper();
    }

    pub fn helper(&self) {
        self.worker.run();
    }
}
`,
	})
	p := NewProvider(RustSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	direct := nodeByNameKind(t, g, "direct", graph.KindMethod)
	helper := nodeByNameKind(t, g, "helper", graph.KindMethod)
	if callEdgeTo(g, direct.ID, helper.ID) == nil {
		t.Fatalf("self.helper() not resolved; edges: %v", g.GetOutEdges(direct.ID))
	}
	run := nodeByNameKind(t, g, "run", graph.KindMethod)
	if callEdgeTo(g, helper.ID, run.ID) == nil {
		t.Fatalf("self.worker.run() not resolved through field type; edges: %v", g.GetOutEdges(helper.ID))
	}
}

// A local initialised from a bare call takes the callee's declared
// return type.
func TestRust_FunctionReturnTypePropagates(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"src/engine.rs": rustSvc,
		"src/app.rs": `use crate::engine::Svc;

pub fn build() -> Svc {
    Svc { count: 0 }
}

pub fn main() {
    let s = build();
    s.run();
}
`,
	})
	p := NewProvider(RustSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "main", graph.KindFunction)
	run := nodeByNameKind(t, g, "run", graph.KindMethod)
	if callEdgeTo(g, caller.ID, run.ID) == nil {
		t.Fatalf("return-type-propagated call not resolved; edges: %v", g.GetOutEdges(caller.ID))
	}
}

func TestRust_AmbiguousReceiverStaysUntouched(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"src/engine.rs": rustSvc,
		"src/alt.rs": `pub struct Alt {
    id: u32,
}

impl Alt {
    pub fn run(&self) {}
}
`,
		"src/app.rs": `use crate::alt::Alt;
use crate::engine::Svc;

pub fn main() {
    let mut s = Svc::new();
    s = Alt::new();
    s.run();
}
`,
	})
	p := NewProvider(RustSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "main", graph.KindFunction)
	assertUntouched(t, g, caller.ID, "run", "rust-types")
}
