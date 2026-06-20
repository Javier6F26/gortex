package indexer

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

func TestContentLinkEdgeBudget(t *testing.T) {
	require.Equal(t, 2000, contentLinkEdgeBudget(0))
	require.Equal(t, 2000, contentLinkEdgeBudget(10000), "10% (1000) is below the 2000 floor")
	require.Equal(t, 5000, contentLinkEdgeBudget(50000), "10% of live edges above the floor")
}

func TestLinkContentToCode(t *testing.T) {
	g := graph.New()
	g.AddBatch([]*graph.Node{
		{ID: "pkg/order.go::ProcessOrder", Kind: graph.KindFunction, Name: "ProcessOrder", FilePath: "pkg/order.go"},
		{ID: "deck.pptx::doc:slide-1", Kind: graph.KindDoc, FilePath: "deck.pptx",
			Meta: map[string]any{"data_class": "content", "asset_kind": "slide",
				"section_text": "This deck explains why ProcessOrder validates inventory before charging."}},
		// Markdown prose has no data_class — it must NOT be linked by the content pass.
		{ID: "README.md::doc:intro", Kind: graph.KindDoc, FilePath: "README.md",
			Meta: map[string]any{"asset_kind": "markdown_section", "section_text": "ProcessOrder is the core."}},
	}, nil)

	idx := New(g, parser.NewRegistry(), config.Default().Index, zap.NewNop())
	idx.linkContentToCode()

	var motivates []*graph.Edge
	for _, e := range g.AllEdges() {
		if e.Kind == graph.EdgeMotivates {
			motivates = append(motivates, e)
		}
	}
	require.Len(t, motivates, 1, "the content chunk links; markdown prose (no data_class) does not")
	require.Equal(t, "deck.pptx::doc:slide-1", motivates[0].From)
	require.Equal(t, "pkg/order.go::ProcessOrder", motivates[0].To)
	require.Equal(t, graph.OriginTextMatched, motivates[0].Origin)
	require.Equal(t, "lexical", motivates[0].Meta["signal"])
}
