package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

func TestRExtractor_Functions(t *testing.T) {
	src := []byte(`add <- function(a, b) {
  a + b
}

multiply = function(x, y) {
  x * y
}
`)
	e := NewRExtractor()
	result, err := e.Extract("math.R", src)
	require.NoError(t, err)

	funcs := nodesOfKind(result.Nodes, graph.KindFunction)
	names := make([]string, len(funcs))
	for i, f := range funcs {
		names[i] = f.Name
	}
	assert.Contains(t, names, "add")
	assert.Contains(t, names, "multiply")
}

func TestRExtractor_Imports(t *testing.T) {
	src := []byte(`library(ggplot2)
require(dplyr)
source("utils.R")
`)
	e := NewRExtractor()
	result, err := e.Extract("main.R", src)
	require.NoError(t, err)

	imports := edgesOfKind(result.Edges, graph.EdgeImports)
	assert.Len(t, imports, 3)
}

func TestRExtractor_Variables(t *testing.T) {
	src := []byte(`max_size <- 100
threshold = 0.5
name <- "test"
`)
	e := NewRExtractor()
	result, err := e.Extract("config.R", src)
	require.NoError(t, err)

	vars := nodesOfKind(result.Nodes, graph.KindVariable)
	varNames := make([]string, len(vars))
	for i, v := range vars {
		varNames[i] = v.Name
	}
	assert.Contains(t, varNames, "max_size")
	assert.Contains(t, varNames, "threshold")
	assert.Contains(t, varNames, "name")
}
