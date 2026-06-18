package resolver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

func ccEdgeBetween(g graph.Store, from, to string) *graph.Edge {
	for _, e := range g.GetOutEdges(from) {
		if e.To == to && e.Kind == graph.EdgeCalls && e.Meta != nil {
			if v, _ := e.Meta["via"].(string); v == closureCollectionVia {
				return e
			}
		}
	}
	return nil
}

func ccMethod(g graph.Store, id, name, file string, line int, metaKey, field string) {
	g.AddNode(&graph.Node{
		ID: id, Kind: graph.KindMethod, Name: name, FilePath: file, StartLine: line,
		Language: "swift", Meta: map[string]any{metaKey: field},
	})
}

func TestResolveClosureCollectionCalls_PairsDispatcherToRegistrar(t *testing.T) {
	g := graph.New()
	// Base class iterates `validators`; subclass appends to `validators`.
	ccMethod(g, "Request.swift::Request.didCompleteTask", "didCompleteTask", "Request.swift", 40, "cc_dispatch_field", "validators")
	ccMethod(g, "DataRequest.swift::DataRequest.validate", "validate", "DataRequest.swift", 12, "cc_append_field", "validators")

	n := ResolveClosureCollectionCalls(g)
	assert.Equal(t, 1, n)

	e := ccEdgeBetween(g, "Request.swift::Request.didCompleteTask", "DataRequest.swift::DataRequest.validate")
	require.NotNil(t, e, "dispatcher should reach the cross-class registrar")
	assert.Equal(t, "validators", e.Meta["channel_field"])
	assert.Equal(t, SynthClosureCollection, e.Meta[MetaSynthesizedBy])
	assert.Equal(t, graph.OriginSpeculative, e.Origin)
	assert.Equal(t, true, e.Meta["speculative"])
}

func TestResolveClosureCollectionCalls_NoDispatcherNoEdge(t *testing.T) {
	g := graph.New()
	// An append with no forEach-dispatcher on the same field — no edge.
	ccMethod(g, "a.swift::A.add", "add", "a.swift", 3, "cc_append_field", "items")
	assert.Equal(t, 0, ResolveClosureCollectionCalls(g))
}

func TestResolveClosureCollectionCalls_Idempotent(t *testing.T) {
	g := graph.New()
	ccMethod(g, "a.swift::A.fire", "fire", "a.swift", 5, "cc_dispatch_field", "handlers")
	ccMethod(g, "b.swift::B.register", "register", "b.swift", 9, "cc_append_field", "handlers")

	first := ResolveClosureCollectionCalls(g)
	second := ResolveClosureCollectionCalls(g)
	assert.Equal(t, first, second)

	count := 0
	for e := range g.EdgesByKind(graph.EdgeCalls) {
		if e != nil && e.Meta != nil {
			if v, _ := e.Meta["via"].(string); v == closureCollectionVia {
				count++
			}
		}
	}
	assert.Equal(t, 1, count)
}
