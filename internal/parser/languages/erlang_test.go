package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

func TestErlangExtractor_Functions(t *testing.T) {
	src := []byte(`-module(math).
-export([add/2, multiply/2]).

add(A, B) ->
    A + B.

multiply(A, B) ->
    A * B.
`)
	e := NewErlangExtractor()
	result, err := e.Extract("math.erl", src)
	require.NoError(t, err)

	funcs := nodesOfKind(result.Nodes, graph.KindFunction)
	names := make([]string, len(funcs))
	for i, f := range funcs {
		names[i] = f.Name
	}
	assert.Contains(t, names, "add")
	assert.Contains(t, names, "multiply")
}

func TestErlangExtractor_TypesAndModule(t *testing.T) {
	src := []byte(`-module(server).
-behaviour(gen_server).
-type state() :: #{name => string()}.
-record(config, {host, port}).
`)
	e := NewErlangExtractor()
	result, err := e.Extract("server.erl", src)
	require.NoError(t, err)

	pkgs := nodesOfKind(result.Nodes, graph.KindPackage)
	require.Len(t, pkgs, 1)
	assert.Equal(t, "server", pkgs[0].Name)

	types := nodesOfKind(result.Nodes, graph.KindType)
	typeNames := make([]string, len(types))
	for i, n := range types {
		typeNames[i] = n.Name
	}
	assert.Contains(t, typeNames, "state")
	assert.Contains(t, typeNames, "config")

	implEdges := edgesOfKind(result.Edges, graph.EdgeImplements)
	assert.Len(t, implEdges, 1)
}

func TestErlangExtractor_Imports(t *testing.T) {
	src := []byte(`-module(app).
-import(lists, [map/2, filter/2]).
-import(io, [format/2]).
`)
	e := NewErlangExtractor()
	result, err := e.Extract("app.erl", src)
	require.NoError(t, err)

	imports := edgesOfKind(result.Edges, graph.EdgeImports)
	assert.Len(t, imports, 2)
}
