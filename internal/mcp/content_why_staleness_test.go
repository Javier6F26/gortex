package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/query"
)

// TestWhy_OnLeanContentNode verifies C5: the why-layer still resolves a
// content document that motivates a symbol after the content/code split —
// the lean content node carries name, asset_kind, and a snippet, and the
// EdgeMotivates edge is intact, so the one-hop walk returns the content
// entry unchanged in shape (the snippet stands in for the full body, which
// now lives in the content index).
func TestWhy_OnLeanContentNode(t *testing.T) {
	s := newTestServer(t)
	s.graph.AddBatch([]*graph.Node{
		{
			ID: "spec.pdf::doc:pdf_page-3", Kind: graph.KindDoc, Name: "spec.pdf p.3", FilePath: "spec.pdf",
			Meta: map[string]any{
				"data_class": "content", "asset_kind": "pdf_page",
				"section_text": "the snippet preview", "content_indexed": true,
			},
		},
		{ID: "pkg/x.go::DoThing", Kind: graph.KindFunction, Name: "DoThing", FilePath: "pkg/x.go"},
	}, []*graph.Edge{
		{From: "spec.pdf::doc:pdf_page-3", To: "pkg/x.go::DoThing", Kind: graph.EdgeMotivates,
			Meta: map[string]any{"signal": "names_symbol"}},
	})
	s.engine = query.NewEngine(s.graph)

	entries := s.whyEntriesFor(context.Background(), "pkg/x.go::DoThing")
	require.Len(t, entries, 1)
	require.Equal(t, "content", entries[0].Kind)
	require.Equal(t, "pdf_page", entries[0].AssetKind)
	require.Equal(t, "spec.pdf::doc:pdf_page-3", entries[0].SourceID)
	require.Equal(t, "the snippet preview", entries[0].Text,
		"the why-layer surfaces the lean node's snippet as the motivating text")
	require.Equal(t, "names_symbol", entries[0].Signal)
}

// TestDocStaleness_OnLeanContentNode verifies C5: doc-staleness still flags
// a content document whose referenced symbol is gone, after leaning. It
// reads the source node's File/Name and the EdgeMotivates edges — never the
// body — so the lean node is fully sufficient.
func TestDocStaleness_OnLeanContentNode(t *testing.T) {
	g := graph.New()
	g.AddBatch([]*graph.Node{
		{
			ID: "spec.pdf::doc:pdf_page-0", Kind: graph.KindDoc, Name: "spec.pdf", FilePath: "spec.pdf",
			Meta: map[string]any{"data_class": "content", "asset_kind": "pdf_page", "section_text": "snippet"},
		},
	}, []*graph.Edge{
		// Motivates a symbol that does not exist → dangling.
		{From: "spec.pdf::doc:pdf_page-0", To: "pkg/x.go::Gone", Kind: graph.EdgeMotivates},
	})

	res := analyzeDocStaleness(g, 50)
	require.Equal(t, 1, res.AssessedLinks)
	require.Len(t, res.Stale, 1)
	require.Equal(t, "dangling", res.Stale[0].WorstState)
	require.Equal(t, 1, res.Stale[0].Dangling)
	require.Equal(t, "spec.pdf", res.Stale[0].File)
	require.Equal(t, "spec.pdf::doc:pdf_page-0", res.Stale[0].Source)
}
