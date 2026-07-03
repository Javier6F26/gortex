package resolver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

func phpType(s graph.Store, id, name string, meta map[string]any) {
	s.AddNode(&graph.Node{ID: id, Kind: graph.KindType, Name: name, FilePath: id, Language: "php", Meta: meta})
}
func phpIface(s graph.Store, id, name string, meta map[string]any) {
	s.AddNode(&graph.Node{ID: id, Kind: graph.KindInterface, Name: name, FilePath: id, Language: "php", Meta: meta})
}
func phpMethod(s graph.Store, id, name, receiver string) {
	s.AddNode(&graph.Node{ID: id, Kind: graph.KindMethod, Name: name, FilePath: id, Language: "php",
		Meta: map[string]any{"receiver": receiver}})
}

// Interface-typed receiver: an ambiguous `$h->handle()` fans out to the
// interface declaration + every in-repo implementation, related through the
// `implements` clause (scope_interfaces). Edges land at ast_inferred so they
// are visible in default find_usages.
func TestResolvePHPOverrideDispatch_InterfaceFanOut(t *testing.T) {
	var s graph.Store = graph.New()
	iface := "src/HandlerInterface.php"
	th := "src/TestHandler.php"
	sh := "src/StreamHandler.php"
	app := "src/app.php"

	phpIface(s, iface+"::HandlerInterface", "HandlerInterface", nil)
	phpMethod(s, iface+"::HandlerInterface.handle", "handle", "HandlerInterface")
	phpType(s, th+"::TestHandler", "TestHandler", map[string]any{"scope_interfaces": "HandlerInterface"})
	phpMethod(s, th+"::TestHandler.handle", "handle", "TestHandler")
	phpType(s, sh+"::StreamHandler", "StreamHandler", map[string]any{"scope_interfaces": "HandlerInterface"})
	phpMethod(s, sh+"::StreamHandler.handle", "handle", "StreamHandler")

	caller := app + "::run"
	s.AddNode(&graph.Node{ID: caller, Kind: graph.KindFunction, Name: "run", FilePath: app, Language: "php"})
	s.AddEdge(&graph.Edge{From: caller, To: "unresolved::*.handle", Kind: graph.EdgeCalls, FilePath: app, Line: 5})

	r := New(s)
	n := r.resolvePHPOverrideDispatch()
	assert.Positive(t, n, "at least one dispatch edge should be bound")

	for _, target := range []string{
		iface + "::HandlerInterface.handle",
		th + "::TestHandler.handle",
		sh + "::StreamHandler.handle",
	} {
		var found *graph.Edge
		for _, in := range s.GetInEdges(target) {
			if in.From == caller && in.Kind == graph.EdgeCalls {
				found = in
				break
			}
		}
		require.NotNil(t, found, "expected fan-out call edge run→%s", target)
		assert.Equal(t, graph.OriginASTInferred, found.Origin, "dispatch edge is ast_inferred (visible)")
		assert.Equal(t, "override", found.Meta["dispatch"])
		assert.Equal(t, 5, found.Line, "edge anchored at the call site")
	}
	// The ambiguous stub is gone.
	for _, e := range s.GetOutEdges(caller) {
		assert.NotEqual(t, "unresolved::*.handle", e.To)
	}
}

// parent::__construct() must bind through a 3-deep extends chain (Leaf → Mid →
// Base, only Base declares the constructor), a precise single bind.
func TestResolvePHPOverrideDispatch_ParentConstructChain(t *testing.T) {
	var s graph.Store = graph.New()
	base := "src/Base.php"
	mid := "src/Mid.php"
	leaf := "src/Leaf.php"

	phpType(s, base+"::Base", "Base", nil)
	phpMethod(s, base+"::Base.__construct", "__construct", "Base")
	phpType(s, mid+"::Mid", "Mid", map[string]any{"scope_parent": "Base"})
	phpType(s, leaf+"::Leaf", "Leaf", map[string]any{"scope_parent": "Mid"})
	phpMethod(s, leaf+"::Leaf.__construct", "__construct", "Leaf")

	caller := leaf + "::Leaf.__construct"
	s.AddEdge(&graph.Edge{
		From: caller, To: "unresolved::*.__construct", Kind: graph.EdgeCalls,
		FilePath: leaf, Line: 7, Meta: map[string]any{"scope_kind": "parent"},
	})

	r := New(s)
	r.resolvePHPOverrideDispatch()

	var bound *graph.Edge
	for _, e := range s.GetOutEdges(caller) {
		if e.Kind == graph.EdgeCalls && e.To == base+"::Base.__construct" {
			bound = e
			break
		}
	}
	require.NotNil(t, bound, "parent::__construct must bind to Base.__construct up the 3-deep chain")
	assert.Equal(t, graph.OriginASTInferred, bound.Origin)
	assert.Equal(t, "scope", bound.Meta["dispatch"])
	for _, e := range s.GetOutEdges(caller) {
		assert.NotEqual(t, "unresolved::*.__construct", e.To)
	}
}

// A trait-provided method binds via `use`: Button extends Widget, Widget uses
// trait Renderer (an EdgeExtends), so `$x->render()` fans out to Renderer.render
// (the trait) as well as Button.render — related through the trait in the
// ancestor closure.
func TestResolvePHPOverrideDispatch_TraitProvidedMethod(t *testing.T) {
	var s graph.Store = graph.New()
	trait := "src/Renderer.php"
	widget := "src/Widget.php"
	button := "src/Button.php"
	app := "src/paint.php"

	phpType(s, trait+"::Renderer", "Renderer", map[string]any{"type_flavor": "trait", "kind": "trait"})
	phpMethod(s, trait+"::Renderer.render", "render", "Renderer")
	phpType(s, widget+"::Widget", "Widget", nil)
	// Widget uses the Renderer trait — modelled as an EdgeExtends to the stub.
	s.AddEdge(&graph.Edge{From: widget + "::Widget", To: "unresolved::Renderer", Kind: graph.EdgeExtends,
		FilePath: widget, Line: 3, Meta: map[string]any{"via": "trait"}})
	phpType(s, button+"::Button", "Button", map[string]any{"scope_parent": "Widget"})
	phpMethod(s, button+"::Button.render", "render", "Button")

	caller := app + "::paint"
	s.AddNode(&graph.Node{ID: caller, Kind: graph.KindFunction, Name: "paint", FilePath: app, Language: "php"})
	s.AddEdge(&graph.Edge{From: caller, To: "unresolved::*.render", Kind: graph.EdgeCalls, FilePath: app, Line: 4})

	r := New(s)
	r.resolvePHPOverrideDispatch()

	for _, target := range []string{trait + "::Renderer.render", button + "::Button.render"} {
		var found bool
		for _, in := range s.GetInEdges(target) {
			if in.From == caller && in.Kind == graph.EdgeCalls {
				found = true
			}
		}
		assert.True(t, found, "expected fan-out call edge paint→%s (trait-related)", target)
	}
}

// Precision guard: same-name methods on UNRELATED types (no shared ancestor)
// must never be sprayed together.
func TestResolvePHPOverrideDispatch_UnrelatedNotFannedOut(t *testing.T) {
	var s graph.Store = graph.New()
	a := "src/Alpha.php"
	b := "src/Beta.php"
	app := "src/main.php"

	phpType(s, a+"::Alpha", "Alpha", nil)
	phpMethod(s, a+"::Alpha.save", "save", "Alpha")
	phpType(s, b+"::Beta", "Beta", nil)
	phpMethod(s, b+"::Beta.save", "save", "Beta")

	caller := app + "::go"
	s.AddNode(&graph.Node{ID: caller, Kind: graph.KindFunction, Name: "go", FilePath: app, Language: "php"})
	s.AddEdge(&graph.Edge{From: caller, To: "unresolved::*.save", Kind: graph.EdgeCalls, FilePath: app, Line: 3})

	r := New(s)
	r.resolvePHPOverrideDispatch()

	// Unrelated save() methods share no ancestor → the call stays ambiguous.
	var stillUnresolved bool
	for _, e := range s.GetOutEdges(caller) {
		if e.To == "unresolved::*.save" {
			stillUnresolved = true
		}
	}
	assert.True(t, stillUnresolved, "unrelated same-name methods must not be fanned out")
}

// Idempotency: a second pass over an already-bound graph must not duplicate
// the fan-out edges.
func TestResolvePHPOverrideDispatch_Idempotent(t *testing.T) {
	var s graph.Store = graph.New()
	iface := "src/I.php"
	x := "src/X.php"
	y := "src/Y.php"
	app := "src/a.php"

	phpIface(s, iface+"::I", "I", nil)
	phpMethod(s, iface+"::I.run", "run", "I")
	phpType(s, x+"::X", "X", map[string]any{"scope_interfaces": "I"})
	phpMethod(s, x+"::X.run", "run", "X")
	phpType(s, y+"::Y", "Y", map[string]any{"scope_interfaces": "I"})
	phpMethod(s, y+"::Y.run", "run", "Y")

	caller := app + "::z"
	s.AddNode(&graph.Node{ID: caller, Kind: graph.KindFunction, Name: "z", FilePath: app, Language: "php"})
	s.AddEdge(&graph.Edge{From: caller, To: "unresolved::*.run", Kind: graph.EdgeCalls, FilePath: app, Line: 2})

	r := New(s)
	r.resolvePHPOverrideDispatch()
	countAfterFirst := len(s.GetOutEdges(caller))
	// Re-stub the primary to simulate a restub→re-resolve cycle.
	r.resolvePHPOverrideDispatch()
	assert.Equal(t, countAfterFirst, len(s.GetOutEdges(caller)),
		"re-running the pass must not duplicate fan-out edges")
}
