package dataflow

import (
	"errors"
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

// refineFixtureSrc is the function the refinement tests reason
// about. Binding sites (snippet lines):
//
//	line 1: func F(a int) int   — param a
//	line 2: b := a              — local b@+2 (consumes a)
//	line 3: c := b              — local c@+3 (consumes the live b)
//	line 4: b = 99              — kills b@+2
//	line 5: d := b              — local d@+5 (consumes the NEW b, not b@+2)
//	line 6: return d
const refineFixtureSrc = `func F(a int) int {
	b := a
	c := b
	b = 99
	d := b
	return d
}`

const refineOwner = "main.go::F"

// refineGraph wires the binding nodes and value_flow edges. The
// b@+2 → d@+5 edge is deliberately stale: by line 5 the b binding
// from line 2 has been overwritten, so the reaching-definitions
// analysis must prune it.
func refineGraph(t *testing.T) *graph.Graph {
	t.Helper()
	g := graph.New()
	g.AddNode(&graph.Node{
		ID: refineOwner, Kind: graph.KindFunction, Name: "F",
		FilePath: "main.go", Language: "go", StartLine: 1, EndLine: 7,
	})
	add := func(id string, kind graph.NodeKind, name string, line int) {
		g.AddNode(&graph.Node{
			ID: id, Kind: kind, Name: name,
			FilePath: "main.go", Language: "go", StartLine: line, EndLine: line,
		})
	}
	add(refineOwner+"#param:a", graph.KindParam, "a", 1)
	add(refineOwner+"#local:b@+2", graph.KindLocal, "b", 2)
	add(refineOwner+"#local:c@+3", graph.KindLocal, "c", 3)
	add(refineOwner+"#local:d@+5", graph.KindLocal, "d", 5)

	flow := func(from, to string, line int) {
		g.AddEdge(&graph.Edge{
			From: from, To: to, Kind: graph.EdgeValueFlow,
			FilePath: "main.go", Line: line, Origin: graph.OriginASTResolved,
		})
	}
	// True flows.
	flow(refineOwner+"#param:a", refineOwner+"#local:b@+2", 2)
	flow(refineOwner+"#local:b@+2", refineOwner+"#local:c@+3", 3)
	// Stale flow: b@+2 cannot reach d@+5 (b reassigned at line 4).
	flow(refineOwner+"#local:b@+2", refineOwner+"#local:d@+5", 5)
	return g
}

func fixtureResolver(t *testing.T, calls *int) SourceResolver {
	return func(fn *graph.Node) (FuncSource, error) {
		if calls != nil {
			*calls++
		}
		if fn.ID != refineOwner {
			return FuncSource{}, errors.New("unknown function")
		}
		return FuncSource{Src: []byte(refineFixtureSrc), StartLine: 1}, nil
	}
}

func TestRefinerConfirmsTrueFlow(t *testing.T) {
	g := refineGraph(t)
	e := New(g).WithRefiner(NewRefiner(g, fixtureResolver(t, nil), 0))

	paths := e.FlowBetween(refineOwner+"#param:a", refineOwner+"#local:b@+2", 0, 0)
	if len(paths) != 1 || paths[0].Length() != 1 {
		t.Fatalf("expected one single-hop path, got %+v", paths)
	}
	step := paths[0].Edges[0]
	if step.Refined != RefinedConfirmed {
		t.Errorf("param→local flow should be confirmed, got %q", step.Refined)
	}
}

func TestRefinerConfirmsChainedFlow(t *testing.T) {
	g := refineGraph(t)
	e := New(g).WithRefiner(NewRefiner(g, fixtureResolver(t, nil), 0))

	paths := e.FlowBetween(refineOwner+"#local:b@+2", refineOwner+"#local:c@+3", 0, 0)
	if len(paths) != 1 {
		t.Fatalf("expected one path, got %+v", paths)
	}
	if got := paths[0].Edges[0].Refined; got != RefinedConfirmed {
		t.Errorf("live local→local flow should be confirmed, got %q", got)
	}
}

func TestRefinerPrunesStaleFlow(t *testing.T) {
	g := refineGraph(t)
	e := New(g).WithRefiner(NewRefiner(g, fixtureResolver(t, nil), 0))

	paths := e.FlowBetween(refineOwner+"#local:b@+2", refineOwner+"#local:d@+5", 0, 0)
	if len(paths) != 1 {
		t.Fatalf("expected one path, got %+v", paths)
	}
	step := paths[0].Edges[0]
	if step.Refined != RefinedPruned {
		t.Errorf("stale flow (b reassigned before d := b) should be pruned, got %q", step.Refined)
	}

	// Pruning must cost confidence relative to the unrefined run.
	plain := New(g).FlowBetween(refineOwner+"#local:b@+2", refineOwner+"#local:d@+5", 0, 0)
	if !(paths[0].Confidence < plain[0].Confidence) {
		t.Errorf("pruned path confidence %v must drop below unrefined %v",
			paths[0].Confidence, plain[0].Confidence)
	}
}

func TestRefinerRanksConfirmedAbovePruned(t *testing.T) {
	// Two same-length paths from b@+2 to a common sink — one through
	// the live binding c@+3, one through the stale hop to d@+5. After
	// refinement the pruned path's confidence drops, so the confirmed
	// route must rank first.
	g := refineGraph(t)
	g.AddNode(&graph.Node{
		ID: "main.go::Sink", Kind: graph.KindFunction, Name: "Sink",
		FilePath: "main.go", Language: "go", StartLine: 20, EndLine: 22,
	})
	for _, from := range []string{refineOwner + "#local:c@+3", refineOwner + "#local:d@+5"} {
		g.AddEdge(&graph.Edge{
			From: from, To: "main.go::Sink", Kind: graph.EdgeValueFlow,
			FilePath: "main.go", Origin: graph.OriginASTResolved,
		})
	}
	e := New(g).WithRefiner(NewRefiner(g, fixtureResolver(t, nil), 0))

	paths := e.FlowBetween(refineOwner+"#local:b@+2", "main.go::Sink", 0, 0)
	if len(paths) != 2 {
		t.Fatalf("expected two competing paths, got %+v", paths)
	}
	first, second := paths[0], paths[1]
	if first.Edges[0].Refined != RefinedConfirmed {
		t.Errorf("top-ranked path must start with the confirmed hop, got %q (path %v)", first.Edges[0].Refined, first.IDs)
	}
	if second.Edges[0].Refined != RefinedPruned {
		t.Errorf("second path must carry the pruned marker, got %q (path %v)", second.Edges[0].Refined, second.IDs)
	}
	if !(first.Confidence > second.Confidence) {
		t.Errorf("confirmed path confidence %v must beat pruned %v", first.Confidence, second.Confidence)
	}
}

// A param whose binding ID carries a `@<position>` suffix (the form
// every non-Go extractor mints) must still anchor onto the CFG's
// param statement so the hop is refined, not silently skipped.
func TestRefinerConfirmsPositionalParamFlow(t *testing.T) {
	g := graph.New()
	owner := "shape.go::F"
	g.AddNode(&graph.Node{
		ID: owner, Kind: graph.KindFunction, Name: "F",
		FilePath: "shape.go", Language: "go", StartLine: 1, EndLine: 4,
	})
	// Param ID with a positional disambiguator, as Python/TS/etc emit.
	paramID := owner + "#param:a@0"
	localID := owner + "#local:b@+2"
	g.AddNode(&graph.Node{ID: paramID, Kind: graph.KindParam, Name: "a", FilePath: "shape.go", Language: "go", StartLine: 1, EndLine: 1})
	g.AddNode(&graph.Node{ID: localID, Kind: graph.KindLocal, Name: "b", FilePath: "shape.go", Language: "go", StartLine: 2, EndLine: 2})
	g.AddEdge(&graph.Edge{From: paramID, To: localID, Kind: graph.EdgeValueFlow, FilePath: "shape.go", Line: 2, Origin: graph.OriginASTResolved})

	resolve := func(fn *graph.Node) (FuncSource, error) {
		return FuncSource{Src: []byte("func F(a int) int {\n\tb := a\n\treturn b\n}"), StartLine: 1}, nil
	}
	e := New(g).WithRefiner(NewRefiner(g, resolve, 0))
	paths := e.FlowBetween(paramID, localID, 0, 0)
	if len(paths) != 1 {
		t.Fatalf("expected one path, got %+v", paths)
	}
	if got := paths[0].Edges[0].Refined; got != RefinedConfirmed {
		t.Errorf("positional-param flow must be confirmed, got %q (the @0 suffix must be stripped)", got)
	}
}

// A confirmed path that is LONGER than a competing pruned path must
// still rank ahead of it: confidence demotion alone can't move a
// pruned hop past a shorter confirmed one when length is the primary
// sort key, so pruned paths must sink categorically.
func TestRefinerSinksPrunedBelowLongerConfirmed(t *testing.T) {
	// Reuse the fixture's stale b@+2 → d@+5 hop (pruned, 1 hop) and
	// build a longer confirmed route a → b → c → sink (3 hops) to the
	// same sink, so the pruned path is strictly shorter.
	g := refineGraph(t)
	g.AddNode(&graph.Node{
		ID: "main.go::Sink", Kind: graph.KindFunction, Name: "Sink",
		FilePath: "main.go", Language: "go", StartLine: 20, EndLine: 22,
	})
	// Shorter pruned route: b@+2 → d@+5 → Sink (2 hops, first hop pruned).
	g.AddEdge(&graph.Edge{From: refineOwner + "#local:d@+5", To: "main.go::Sink", Kind: graph.EdgeValueFlow, FilePath: "main.go", Origin: graph.OriginASTResolved})
	// Longer confirmed route: b@+2 → c@+3 → Sink (2 hops) — but make it
	// genuinely longer by routing through an extra confirmed local.
	// c@+3 is live, so b→c is confirmed; c→Sink is out of scope
	// (unmarked, not pruned). Add an intermediate to force length 3.
	g.AddNode(&graph.Node{ID: "main.go::Mid", Kind: graph.KindFunction, Name: "Mid", FilePath: "main.go", Language: "go", StartLine: 15, EndLine: 16})
	g.AddEdge(&graph.Edge{From: refineOwner + "#local:c@+3", To: "main.go::Mid", Kind: graph.EdgeValueFlow, FilePath: "main.go", Origin: graph.OriginASTResolved})
	g.AddEdge(&graph.Edge{From: "main.go::Mid", To: "main.go::Sink", Kind: graph.EdgeValueFlow, FilePath: "main.go", Origin: graph.OriginASTResolved})

	e := New(g).WithRefiner(NewRefiner(g, fixtureResolver(t, nil), 0))
	paths := e.FlowBetween(refineOwner+"#local:b@+2", "main.go::Sink", 0, 0)
	if len(paths) < 2 {
		t.Fatalf("expected at least two competing paths, got %+v", paths)
	}
	// The shortest path is the 2-hop pruned route; the confirmed route
	// is 3 hops. Despite being longer, the confirmed path must rank
	// first because the pruned path sinks.
	top := paths[0]
	if hasPrunedHop(top) {
		t.Errorf("top-ranked path must not be the pruned one; got pruned path %v ranked first", top.IDs)
	}
	// And the pruned path must still be present, just demoted.
	sawPruned := false
	for _, p := range paths {
		if hasPrunedHop(p) {
			sawPruned = true
		}
	}
	if !sawPruned {
		t.Fatalf("the pruned path must remain in the result set (demoted, not dropped): %+v", paths)
	}
}

func TestRefinerCachesPerFunctionCFG(t *testing.T) {
	g := refineGraph(t)
	calls := 0
	e := New(g).WithRefiner(NewRefiner(g, fixtureResolver(t, &calls), 0))

	// Two hops inside the same function: a→b→c. One CFG build.
	paths := e.FlowBetween(refineOwner+"#param:a", refineOwner+"#local:c@+3", 0, 0)
	if len(paths) != 1 || paths[0].Length() != 2 {
		t.Fatalf("expected one two-hop path, got %+v", paths)
	}
	if calls != 1 {
		t.Errorf("CFG source resolved %d times, want 1 (cached per function)", calls)
	}
	for _, step := range paths[0].Edges {
		if step.Refined != RefinedConfirmed {
			t.Errorf("hop %s→%s should be confirmed, got %q", step.From, step.To, step.Refined)
		}
	}
}

func TestRefinerLeavesOutOfScopeHopsUnmarked(t *testing.T) {
	// Function-level nodes (no #local:/#param: binding form) are out
	// of refinement scope and stay unmarked.
	g := graph.New()
	for _, id := range []string{"A", "B"} {
		g.AddNode(&graph.Node{ID: id, Kind: graph.KindFunction, Name: id, FilePath: "x.go", Language: "go", StartLine: 1, EndLine: 2})
	}
	g.AddEdge(&graph.Edge{From: "A", To: "B", Kind: graph.EdgeValueFlow, FilePath: "x.go", Origin: graph.OriginASTResolved})

	resolverCalls := 0
	e := New(g).WithRefiner(NewRefiner(g, fixtureResolver(t, &resolverCalls), 0))
	paths := e.FlowBetween("A", "B", 0, 0)
	if len(paths) != 1 {
		t.Fatalf("expected one path, got %+v", paths)
	}
	if got := paths[0].Edges[0].Refined; got != "" {
		t.Errorf("cross-symbol hop must stay unmarked, got %q", got)
	}
	if resolverCalls != 0 {
		t.Errorf("out-of-scope hops must not trigger CFG builds (%d calls)", resolverCalls)
	}
}

func TestRefinerSurvivesResolverFailure(t *testing.T) {
	g := refineGraph(t)
	failing := func(fn *graph.Node) (FuncSource, error) {
		return FuncSource{}, errors.New("disk gone")
	}
	e := New(g).WithRefiner(NewRefiner(g, failing, 0))
	paths := e.FlowBetween(refineOwner+"#param:a", refineOwner+"#local:b@+2", 0, 0)
	if len(paths) != 1 {
		t.Fatalf("paths must survive resolver failure, got %+v", paths)
	}
	if got := paths[0].Edges[0].Refined; got != "" {
		t.Errorf("unresolvable source must leave hops unmarked, got %q", got)
	}
}

func TestSplitBindingID(t *testing.T) {
	cases := []struct {
		id          string
		owner, name string
		ok          bool
	}{
		{"f.go::F#local:x@+3", "f.go::F", "x", true},
		{"f.go::F#param:in", "f.go::F", "in", true},
		// Non-Go extractors (Python / TS / Rust / Java / C#) append a
		// position disambiguator to param IDs; the suffix must be
		// stripped so the bare name matches the CFG's def/use sets.
		{"a.py::f#param:limit@0", "a.py::f", "limit", true},
		{"a.ts::f#param:limit@2", "a.ts::f", "limit", true},
		{"f.go::F", "", "", false},
		{"unresolved::X", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		owner, name, ok := splitBindingID(c.id)
		if owner != c.owner || name != c.name || ok != c.ok {
			t.Errorf("splitBindingID(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.id, owner, name, ok, c.owner, c.name, c.ok)
		}
	}
}
