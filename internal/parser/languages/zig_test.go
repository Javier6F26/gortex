package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

func TestZigExtractor_Functions(t *testing.T) {
	src := []byte(`
pub fn add(a: i32, b: i32) i32 {
    return a + b;
}

fn helper() void {
    doWork();
}
`)
	e := NewZigExtractor()
	result, err := e.Extract("math.zig", src)
	require.NoError(t, err)

	funcs := nodesOfKind(result.Nodes, graph.KindFunction)
	names := make([]string, len(funcs))
	for i, f := range funcs {
		names[i] = f.Name
	}
	assert.Contains(t, names, "add")
	assert.Contains(t, names, "helper")
}

func TestZigExtractor_TypesAndImports(t *testing.T) {
	src := []byte(`
const std = @import("std");
const os = @import("os");

pub const Point = struct {
    x: f64,
    y: f64,
};

const Color = enum {
    red,
    green,
    blue,
};
`)
	e := NewZigExtractor()
	result, err := e.Extract("types.zig", src)
	require.NoError(t, err)

	types := nodesOfKind(result.Nodes, graph.KindType)
	typeNames := make([]string, len(types))
	for i, n := range types {
		typeNames[i] = n.Name
	}
	assert.Contains(t, typeNames, "Point")
	assert.Contains(t, typeNames, "Color")

	imports := edgesOfKind(result.Edges, graph.EdgeImports)
	assert.Len(t, imports, 2)
}

func TestZigExtractor_Variables(t *testing.T) {
	src := []byte(`
const MAX_SIZE = 1024;
var counter = 0;
`)
	e := NewZigExtractor()
	result, err := e.Extract("config.zig", src)
	require.NoError(t, err)

	vars := nodesOfKind(result.Nodes, graph.KindVariable)
	varNames := make([]string, len(vars))
	for i, v := range vars {
		varNames[i] = v.Name
	}
	assert.Contains(t, varNames, "MAX_SIZE")
	assert.Contains(t, varNames, "counter")
}
