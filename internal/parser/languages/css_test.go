package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

func TestCSSExtractor_Selectors(t *testing.T) {
	src := []byte(`.container {
  display: flex;
}

#main-content {
  padding: 20px;
}

:root {
  --primary-color: #333;
}
`)
	e := NewCSSExtractor()
	result, err := e.Extract("style.css", src)
	require.NoError(t, err)

	// Should have file node at minimum.
	assert.GreaterOrEqual(t, len(result.Nodes), 1)
}

func TestCSSExtractor_Import(t *testing.T) {
	src := []byte(`@import url("reset.css");
@import "theme.css";
`)
	e := NewCSSExtractor()
	result, err := e.Extract("style.css", src)
	require.NoError(t, err)

	imports := edgesOfKind(result.Edges, graph.EdgeImports)
	assert.GreaterOrEqual(t, len(imports), 1)
}

func TestCSSExtractor_ClassSelector(t *testing.T) {
	src := []byte(`.btn {
  padding: 10px;
}
.btn-primary {
  color: blue;
}
`)
	e := NewCSSExtractor()
	result, err := e.Extract("buttons.css", src)
	require.NoError(t, err)

	types := nodesOfKind(result.Nodes, graph.KindType)
	assert.GreaterOrEqual(t, len(types), 2)
}

func TestCSSExtractor_IDSelector(t *testing.T) {
	src := []byte(`#header {
  height: 60px;
}
#footer {
  height: 40px;
}
`)
	e := NewCSSExtractor()
	result, err := e.Extract("layout.css", src)
	require.NoError(t, err)

	vars := nodesOfKind(result.Nodes, graph.KindVariable)
	assert.GreaterOrEqual(t, len(vars), 2)
}

func TestCSSExtractor_CustomProperties(t *testing.T) {
	src := []byte(`:root {
  --primary-color: #333;
  --font-size: 16px;
}
`)
	e := NewCSSExtractor()
	result, err := e.Extract("vars.css", src)
	require.NoError(t, err)

	vars := nodesOfKind(result.Nodes, graph.KindVariable)
	assert.GreaterOrEqual(t, len(vars), 2)
}
