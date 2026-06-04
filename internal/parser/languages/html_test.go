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

func TestHTMLExtractor_IDAnchorsAsDocSections(t *testing.T) {
	src := []byte(`<html>
<body>
  <div id="main-content">Hello world</div>
  <form id="login-form">
    <input id="username" type="text">
  </form>
</body>
</html>
`)
	e := NewHTMLExtractor()
	result, err := e.Extract("index.html", src)
	require.NoError(t, err)

	// id-anchored elements are DocSection (KindDoc) nodes now, not KindVariable.
	docs := nodesOfKind(result.Nodes, graph.KindDoc)
	byName := map[string]*graph.Node{}
	for _, n := range docs {
		byName[n.Name] = n
	}
	require.Contains(t, byName, "#main-content")
	assert.Equal(t, "index.html::doc:#main-content", byName["#main-content"].ID)
	assert.Equal(t, "Hello world", byName["#main-content"].Meta["section_text"])
	assert.Equal(t, true, byName["#main-content"].Meta["html_anchor"])
	assert.Contains(t, byName, "#login-form")
	assert.Contains(t, byName, "#username")
	// No id anchors should leak out as the old KindVariable shape.
	assert.Empty(t, nodesOfKind(result.Nodes, graph.KindVariable))
}

func TestHTMLExtractor_InlineScript(t *testing.T) {
	src := []byte(`<html>
<body>
<script type="application/json">{"not":"javascript"}</script>
<script>
function greet(name) {
  return "hi " + name;
}
const who = greet("world");
</script>
</body>
</html>
`)
	e := NewHTMLExtractor()
	result, err := e.Extract("page.html", src)
	require.NoError(t, err)

	// The inline JS function is extracted and owned by the HTML file.
	var greet *graph.Node
	for _, n := range result.Nodes {
		if n.Kind == graph.KindFunction && n.Name == "greet" {
			greet = n
		}
	}
	require.NotNil(t, greet, "inline <script> function greet should be extracted")
	assert.Equal(t, "page.html", greet.FilePath)
	assert.Equal(t, true, greet.Meta["inline_script"])
	// greet is declared on line 5 of the file (script body offset applied).
	assert.Equal(t, 5, greet.StartLine)

	// The HTML file defines the inline function.
	var defines bool
	for _, ed := range result.Edges {
		if ed.Kind == graph.EdgeDefines && ed.From == "page.html" && ed.To == greet.ID {
			defines = true
		}
	}
	assert.True(t, defines, "HTML file should define the inline-script function")

	// The application/json block must NOT be parsed as JavaScript.
	for _, n := range result.Nodes {
		assert.NotEqual(t, "not", n.Name, "JSON data block must not yield JS nodes")
	}
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
