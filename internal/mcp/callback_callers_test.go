package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

// navTestEng adapts a graph.Store to the engineLike surface navCallers needs.
type navTestEng struct{ g graph.Store }

func (e navTestEng) GetSymbol(id string) *graph.Node     { return e.g.GetNode(id) }
func (e navTestEng) GetOutEdges(id string) []*graph.Edge { return e.g.GetOutEdges(id) }
func (e navTestEng) GetInEdges(id string) []*graph.Edge  { return e.g.GetInEdges(id) }

// TestCallbackRegistrationCallers is part of the C3 named set: a function
// registered as a callback (a callback-registration reference edge) shows up
// among its callers — recovering an invoker that static call extraction can't
// see — and the edge classifies as the "callback" reference context.
func TestCallbackRegistrationCallers(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{ID: "s.go::handler", Kind: graph.KindFunction, Name: "handler", FilePath: "s.go"})
	g.AddNode(&graph.Node{ID: "s.go::setup", Kind: graph.KindFunction, Name: "setup", FilePath: "s.go"})
	g.AddNode(&graph.Node{ID: "s.go::direct", Kind: graph.KindFunction, Name: "direct", FilePath: "s.go"})

	// direct calls handler outright; setup only registers it as a callback.
	g.AddEdge(&graph.Edge{From: "s.go::direct", To: "s.go::handler", Kind: graph.EdgeCalls, FilePath: "s.go"})
	cbEdge := &graph.Edge{
		From: "s.go::setup", To: "s.go::handler", Kind: graph.EdgeReferences, FilePath: "s.go",
		Meta: map[string]any{"via": "callback_registration"},
	}
	g.AddEdge(cbEdge)

	callers := navCallers(navTestEng{g}, "s.go::handler")
	ids := map[string]bool{}
	for _, n := range callers {
		ids[n.ID] = true
	}
	assert.True(t, ids["s.go::direct"], "the direct caller is present")
	assert.True(t, ids["s.go::setup"], "the callback registrar must appear as a caller")

	// The callback edge classifies as the callback reference context.
	require.Equal(t, graph.RefContextCallback, graph.RefContextOf(cbEdge, graph.KindFunction))
}
