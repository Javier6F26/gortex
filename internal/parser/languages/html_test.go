package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

func TestHTMLExtractor_ScriptImport(t *testing.T) {
	src := []byte(`<!DOCTYPE html>
<html>
<head>
  <link rel="stylesheet" href="style.css">
  <script src="app.js"></script>
</head>
<body></body>
</html>
`)
	e := NewHTMLExtractor()
	result, err := e.Extract("index.html", src)
	require.NoError(t, err)

	imports := edgesOfKind(result.Edges, graph.EdgeImports)
	assert.GreaterOrEqual(t, len(imports), 1)
}

func TestHTMLExtractor_LinkImport(t *testing.T) {
	src := []byte(`<html>
<head>
  <link rel="stylesheet" href="main.css">
</head>
<body></body>
</html>
`)
	e := NewHTMLExtractor()
	result, err := e.Extract("index.html", src)
	require.NoError(t, err)

	imports := edgesOfKind(result.Edges, graph.EdgeImports)
	require.Len(t, imports, 1)
	assert.Contains(t, imports[0].To, "main.css")
}

func TestHTMLExtractor_IDAttribute(t *testing.T) {
	src := []byte(`<html>
<body>
  <div id="main-content">Hello</div>
  <form id="login-form">
    <input id="username" type="text">
  </form>
</body>
</html>
`)
	e := NewHTMLExtractor()
	result, err := e.Extract("index.html", src)
	require.NoError(t, err)

	vars := nodesOfKind(result.Nodes, graph.KindVariable)
	assert.GreaterOrEqual(t, len(vars), 1)
}

func TestHTMLExtractor_FileNode(t *testing.T) {
	src := []byte(`<html><body>Hello</body></html>`)
	e := NewHTMLExtractor()
	result, err := e.Extract("page.html", src)
	require.NoError(t, err)

	files := nodesOfKind(result.Nodes, graph.KindFile)
	require.Len(t, files, 1)
	assert.Equal(t, "page.html", files[0].Name)
	assert.Equal(t, "html", files[0].Language)
}
