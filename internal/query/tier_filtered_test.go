package query

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

// A min_tier that filters every edge while lower-tier edges exist records a
// tier_filtered caveat instead of leaving a bare empty result that reads as
// "no usages".
func TestFilterByMinTier_TierFilteredCaveat(t *testing.T) {
	sg := &SubGraph{Edges: []*graph.Edge{
		{From: "a", To: "t", Kind: graph.EdgeCalls, Origin: graph.OriginTextMatched},
		{From: "b", To: "t", Kind: graph.EdgeCalls, Origin: graph.OriginASTInferred},
	}}
	sg.FilterByMinTier("lsp_resolved")

	assert.Empty(t, sg.Edges)
	require.NotNil(t, sg.TierFiltered)
	assert.Equal(t, graph.TierFilteredClass, sg.TierFiltered.Class)
	assert.Equal(t, 2, sg.TierFiltered.EdgesBelowMinTier)
	// ast_inferred (rank 3) outranks text_matched (rank 2), so it is the best
	// tier actually available below lsp_resolved.
	assert.Equal(t, graph.OriginASTInferred, sg.TierFiltered.MaxAvailableTier)
}

// When some edges survive the filter, no caveat is attached — the surviving
// rows are their own signal.
func TestFilterByMinTier_NoCaveatWhenEdgesSurvive(t *testing.T) {
	sg := &SubGraph{Edges: []*graph.Edge{
		{From: "a", To: "t", Kind: graph.EdgeCalls, Origin: graph.OriginLSPResolved},
		{From: "b", To: "t", Kind: graph.EdgeCalls, Origin: graph.OriginTextMatched},
	}}
	sg.FilterByMinTier("lsp_resolved")

	assert.Len(t, sg.Edges, 1)
	assert.Nil(t, sg.TierFiltered, "caveat only when the filter empties the visible set")
}

// No min_tier is a no-op — no caveat, all edges kept.
func TestFilterByMinTier_EmptyTierIsNoop(t *testing.T) {
	sg := &SubGraph{Edges: []*graph.Edge{
		{From: "a", To: "t", Kind: graph.EdgeCalls, Origin: graph.OriginTextMatched},
	}}
	sg.FilterByMinTier("")
	assert.Len(t, sg.Edges, 1)
	assert.Nil(t, sg.TierFiltered)
}
