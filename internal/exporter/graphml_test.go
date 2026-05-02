package exporter

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

func TestGraphML_WellFormedXML(t *testing.T) {
	g := buildSampleGraph(t)

	var buf bytes.Buffer
	_, err := WriteGraphML(&buf, g, Options{})
	require.NoError(t, err)

	// Decode into a minimal struct just to assert XML well-formedness +
	// that the root and graph elements are present.
	type data struct {
		XMLName xml.Name `xml:"data"`
		Key     string   `xml:"key,attr"`
		Value   string   `xml:",chardata"`
	}
	type node struct {
		XMLName xml.Name `xml:"node"`
		ID      string   `xml:"id,attr"`
		Data    []data   `xml:"data"`
	}
	type edge struct {
		XMLName xml.Name `xml:"edge"`
		ID      string   `xml:"id,attr"`
		Source  string   `xml:"source,attr"`
		Target  string   `xml:"target,attr"`
		Data    []data   `xml:"data"`
	}
	type graphE struct {
		XMLName xml.Name `xml:"graph"`
		Default string   `xml:"edgedefault,attr"`
		Nodes   []node   `xml:"node"`
		Edges   []edge   `xml:"edge"`
	}
	type graphml struct {
		XMLName xml.Name `xml:"graphml"`
		Graph   graphE   `xml:"graph"`
	}

	var doc graphml
	require.NoError(t, xml.Unmarshal(buf.Bytes(), &doc))
	assert.Equal(t, "directed", doc.Graph.Default)
	assert.Len(t, doc.Graph.Nodes, 3)
	assert.Len(t, doc.Graph.Edges, 2)
}

func TestGraphML_NodeProperties(t *testing.T) {
	g := buildSampleGraph(t)

	var buf bytes.Buffer
	_, err := WriteGraphML(&buf, g, Options{})
	require.NoError(t, err)

	out := buf.String()
	// Required strongly-typed keys.
	assert.Contains(t, out, `<key id="name" for="node"`)
	assert.Contains(t, out, `<key id="kind" for="node"`)
	assert.Contains(t, out, `<key id="confidence" for="edge"`)

	// Per-node data.
	assert.Contains(t, out, `<data key="name">F</data>`)
	assert.Contains(t, out, `<data key="kind">function</data>`)
	assert.Contains(t, out, `<data key="qual_name">example.com/m.F</data>`)
	assert.Contains(t, out, `<data key="start_line">3</data>`)

	// Free-form Meta as JSON.
	assert.Contains(t, out, `<data key="node_meta">`)
	assert.Contains(t, out, `&#34;visibility&#34;:&#34;public&#34;`)
}

func TestGraphML_EdgeProperties(t *testing.T) {
	g := buildSampleGraph(t)

	var buf bytes.Buffer
	_, err := WriteGraphML(&buf, g, Options{})
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `<data key="edge_kind">calls</data>`)
	assert.Contains(t, out, `<data key="confidence">1</data>`)
	assert.Contains(t, out, `<data key="confidence_label">EXTRACTED</data>`)
	assert.Contains(t, out, `<data key="origin">ast_resolved</data>`)
	assert.Contains(t, out, `<data key="edge_line">4</data>`)
}

func TestGraphML_XMLEscaping(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{
		ID: "<dangerous>::id", Kind: graph.KindFunction, Name: `<F & "G">`,
		FilePath: "x.go", StartLine: 1, EndLine: 1, Language: "go",
	})

	var buf bytes.Buffer
	_, err := WriteGraphML(&buf, g, Options{})
	require.NoError(t, err)

	out := buf.String()
	// Reserved characters must be escaped inside attributes and data bodies.
	assert.NotContains(t, out, `<F & "G">`)
	assert.Contains(t, out, "&amp;")
}

func TestGraphML_EmptyGraph(t *testing.T) {
	g := graph.New()
	var buf bytes.Buffer
	stats, err := WriteGraphML(&buf, g, Options{})
	require.NoError(t, err)
	assert.Equal(t, 0, stats.NodesWritten)
	assert.Equal(t, 0, stats.EdgesWritten)
	out := buf.String()
	assert.True(t, strings.HasPrefix(out, `<?xml`))
	assert.Contains(t, out, "<graphml")
	assert.Contains(t, out, `<graph id="G" edgedefault="directed">`)
	assert.Contains(t, out, "</graph>")
}

func TestGraphML_FilterRemovesDanglingEdges(t *testing.T) {
	g := buildSampleGraph(t)

	var buf bytes.Buffer
	_, err := WriteGraphML(&buf, g, Options{Kinds: []graph.NodeKind{graph.KindFunction}})
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `<data key="kind">function</data>`)
	assert.NotContains(t, out, `<data key="kind">method</data>`)
	assert.NotContains(t, out, `<edge `,
		"edges with excluded endpoints must be dropped")
}
