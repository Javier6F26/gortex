package resolver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

func reactSetStateEdgeBetween(g graph.Store, from, to string) *graph.Edge {
	for _, e := range g.GetOutEdges(from) {
		if e.To == to && e.Kind == graph.EdgeCalls && e.Meta != nil {
			if v, _ := e.Meta["via"].(string); v == reactSetStateVia {
				return e
			}
		}
	}
	return nil
}

// reactClass wires a class with the given methods (member_of edges) and the
// setState-callers' call edges.
func reactClass(g graph.Store, file, class string, methods []string, setStateCallers map[string]bool) {
	g.AddNode(&graph.Node{ID: file + "::" + class, Kind: graph.KindType, Name: class, FilePath: file})
	for i, m := range methods {
		id := file + "::" + class + "." + m
		g.AddNode(&graph.Node{ID: id, Kind: graph.KindMethod, Name: m, FilePath: file, StartLine: 5 + i})
		g.AddEdge(&graph.Edge{From: id, To: file + "::" + class, Kind: graph.EdgeMemberOf})
		if setStateCallers[m] {
			g.AddEdge(&graph.Edge{From: id, To: "unresolved::*.setState", Kind: graph.EdgeCalls, FilePath: file, Line: 6 + i})
		}
	}
}

func TestResolveReactSetState_LinksSetterToRender(t *testing.T) {
	g := graph.New()
	reactClass(g, "Counter.tsx", "Counter",
		[]string{"increment", "render", "noop"},
		map[string]bool{"increment": true})

	n := ResolveReactSetStateCalls(g)
	assert.Equal(t, 1, n)

	e := reactSetStateEdgeBetween(g, "Counter.tsx::Counter.increment", "Counter.tsx::Counter.render")
	require.NotNil(t, e, "increment (calls setState) should reach render")
	assert.Equal(t, "Counter.tsx::Counter", e.Meta["component_class"])
	assert.Equal(t, SynthReactSetState, e.Meta[MetaSynthesizedBy])

	// A sibling that never calls setState gets no edge.
	assert.Nil(t, reactSetStateEdgeBetween(g, "Counter.tsx::Counter.noop", "Counter.tsx::Counter.render"))
}

func TestResolveReactSetState_NoRenderNoEdge(t *testing.T) {
	g := graph.New()
	// A class with a setState caller but no render method — no hop.
	reactClass(g, "Svc.ts", "Svc", []string{"update"}, map[string]bool{"update": true})
	assert.Equal(t, 0, ResolveReactSetStateCalls(g))
}

func TestResolveReactSetState_RenderCallingSetStateNoSelfEdge(t *testing.T) {
	g := graph.New()
	// render itself calling setState must not self-link.
	reactClass(g, "C.tsx", "C", []string{"render"}, map[string]bool{"render": true})
	assert.Equal(t, 0, ResolveReactSetStateCalls(g))
	assert.Nil(t, reactSetStateEdgeBetween(g, "C.tsx::C.render", "C.tsx::C.render"))
}

func TestResolveReactSetState_Idempotent(t *testing.T) {
	g := graph.New()
	reactClass(g, "Counter.tsx", "Counter", []string{"increment", "render"}, map[string]bool{"increment": true})
	first := ResolveReactSetStateCalls(g)
	second := ResolveReactSetStateCalls(g)
	assert.Equal(t, first, second)

	count := 0
	for e := range g.EdgesByKind(graph.EdgeCalls) {
		if e != nil && e.Meta != nil {
			if v, _ := e.Meta["via"].(string); v == reactSetStateVia {
				count++
			}
		}
	}
	assert.Equal(t, 1, count)
}
