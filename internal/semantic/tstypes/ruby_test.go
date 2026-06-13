package tstypes

import (
	"testing"

	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/graph"
)

const rubySvc = `class Svc
  def run
  end

  def stop
  end
end
`

// Ruby has no annotations and no name-binding imports — constructor
// inference plus repo-unique name resolution carries the cross-file
// case.
func TestRuby_ConstructorInferenceResolvesCrossFile(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"lib/svc.rb": rubySvc,
		"lib/app.rb": `class App
  def main
    s = Svc.new
    s.run
  end
end
`,
	})
	p := NewProvider(RubySpec(), zap.NewNop())
	res, err := p.Enrich(g, dir)
	if err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "main", graph.KindMethod)
	target := nodeByNameKind(t, g, "run", graph.KindMethod)
	e := callEdgeTo(g, caller.ID, target.ID)
	if e == nil {
		t.Fatalf("constructor-inferred call not resolved; edges: %v", g.GetOutEdges(caller.ID))
	}
	assertASTProvenance(t, e, "ruby-types")
	if res.EdgesConfirmed+res.EdgesAdded == 0 {
		t.Errorf("result reported no edge work: %+v", res)
	}
}

// self-qualified calls and `@ivar = Const.new` receivers resolve
// in-class.
func TestRuby_SelfAndIvarReceivers(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"lib/svc.rb": rubySvc,
		"lib/app.rb": `class App
  def initialize
    @worker = Svc.new
  end

  def direct
    self.helper
  end

  def helper
    @worker.run
  end
end
`,
	})
	p := NewProvider(RubySpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	direct := nodeByNameKind(t, g, "direct", graph.KindMethod)
	helper := nodeByNameKind(t, g, "helper", graph.KindMethod)
	if callEdgeTo(g, direct.ID, helper.ID) == nil {
		t.Fatalf("self.helper not resolved; edges: %v", g.GetOutEdges(direct.ID))
	}
	run := nodeByNameKind(t, g, "run", graph.KindMethod)
	if callEdgeTo(g, helper.ID, run.ID) == nil {
		t.Fatalf("@worker.run not resolved through ivar type; edges: %v", g.GetOutEdges(helper.ID))
	}
}

func TestRuby_SuperclassExtendsSynthesis(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"lib/svc.rb": rubySvc,
		"lib/sub.rb": `class Sub < Svc
  def extra
  end
end
`,
	})
	p := NewProvider(RubySpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	sub := nodeByNameKind(t, g, "Sub", graph.KindType)
	svc := nodeByNameKind(t, g, "Svc", graph.KindType)
	e := edgeBetween(g, sub.ID, graph.EdgeExtends, svc.ID)
	if e == nil {
		t.Fatalf("extends edge missing; edges: %v", g.GetOutEdges(sub.ID))
	}
	assertASTProvenance(t, e, "ruby-types")
}

// `include M` mixes a module in — the module indexes as a package
// node, and the engine still grounds the implements edge against it.
func TestRuby_IncludeModuleImplementsSynthesis(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"lib/greeter.rb": `module Greeter
  def greet
  end
end
`,
		"lib/impl.rb": `class Impl
  include Greeter

  def extra
  end
end
`,
	})
	p := NewProvider(RubySpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	impl := nodeByNameKind(t, g, "Impl", graph.KindType)
	mod := nodeByNameKind(t, g, "Greeter", graph.KindPackage)
	e := edgeBetween(g, impl.ID, graph.EdgeImplements, mod.ID)
	if e == nil {
		t.Fatalf("implements edge missing; edges: %v", g.GetOutEdges(impl.ID))
	}
	assertASTProvenance(t, e, "ruby-types")
}

func TestRuby_AmbiguousReceiverStaysUntouched(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"lib/svc.rb": rubySvc,
		"lib/alt.rb": `class Alt
  def run
  end
end
`,
		"lib/app.rb": `class App
  def main
    s = Svc.new
    s = Alt.new
    s.run
  end
end
`,
	})
	p := NewProvider(RubySpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "main", graph.KindMethod)
	assertUntouched(t, g, caller.ID, "run", "ruby-types")
}
