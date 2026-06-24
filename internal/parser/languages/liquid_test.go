package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

func TestLiquidExtractor_Basics(t *testing.T) {
	src := []byte(`{% assign greeting = 'hello' %}
{% include 'header' %}
{% render 'product-card' %}

{% capture banner %}
  <h1>{{ greeting }}</h1>
{% endcapture %}

{{ banner }}
`)
	e := NewLiquidExtractor()
	require.Equal(t, "liquid", e.Language())

	res, err := e.Extract("page.liquid", src)
	require.NoError(t, err)

	var gotGreeting, gotBanner bool
	for _, n := range res.Nodes {
		switch n.Name {
		case "greeting":
			gotGreeting = true
			assert.Equal(t, graph.KindVariable, n.Kind)
		case "banner":
			gotBanner = true
			assert.Equal(t, graph.KindFunction, n.Kind)
		}
	}
	assert.True(t, gotGreeting)
	assert.True(t, gotBanner)

	var gotInclude, gotRender bool
	for _, ed := range res.Edges {
		if ed.Kind != graph.EdgeImports {
			continue
		}
		switch ed.To {
		case "unresolved::import::snippets/header.liquid":
			gotInclude = true
		case "unresolved::import::snippets/product-card.liquid":
			gotRender = true
		}
	}
	assert.True(t, gotInclude)
	assert.True(t, gotRender)
}

func TestLiquidExtractor_EmptyInput(t *testing.T) {
	res, err := NewLiquidExtractor().Extract("e.liquid", []byte(""))
	require.NoError(t, err)
	require.Len(t, res.Nodes, 1)
	assert.Equal(t, graph.KindFile, res.Nodes[0].Kind)
}

func TestLiquidExtractor_SnippetImportNodes(t *testing.T) {
	src := []byte("{% render 'card' %}\n{% include 'components/list' %}\n{% render 'card' %}\n")
	res, err := NewLiquidExtractor().Extract("templates/index.liquid", src)
	require.NoError(t, err)

	imports := map[string]*graph.Node{}
	for _, n := range res.Nodes {
		if n.Kind == graph.KindImport {
			imports[n.ID] = n
		}
	}
	// One searchable import node per target (the two `render 'card'` dedupe).
	require.Len(t, imports, 2)

	card := imports["templates/index.liquid::import::snippets/card.liquid"]
	require.NotNil(t, card, "render 'card' import node")
	assert.Equal(t, "card", card.Name)
	assert.Equal(t, "render", card.Meta["liquid_tag"])
	assert.Equal(t, "snippets/card.liquid", card.Meta["target"])

	list := imports["templates/index.liquid::import::components/list.liquid"]
	require.NotNil(t, list, "include 'components/list' import node")
	assert.Equal(t, "list", list.Name) // bare name = last path segment
	assert.Equal(t, "include", list.Meta["liquid_tag"])

	// The cross-file EdgeImports resolution is preserved (one per usage site).
	var importEdges int
	for _, e := range res.Edges {
		if e.Kind == graph.EdgeImports && e.To == "unresolved::import::snippets/card.liquid" {
			importEdges++
		}
	}
	assert.Equal(t, 2, importEdges, "both render-card usages keep their EdgeImports")
}
