package semantic

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestEnrichBoundReason(t *testing.T) {
	assert.Equal(t, EnrichBoundBudget, enrichBoundReason(EnrichStatePartial, &EnrichResult{SymbolsTotal: 100, SymbolsCovered: 40}))
	assert.Equal(t, EnrichBoundBudget, enrichBoundReason(EnrichStateCompleted, &EnrichResult{Partial: true, SymbolsTotal: 100, SymbolsCovered: 40}))
	assert.Equal(t, EnrichBoundCap, enrichBoundReason(EnrichStateCompleted, &EnrichResult{SymbolsTotal: 100, SymbolsCovered: 35}))
	assert.Equal(t, EnrichBoundCompletedAll, enrichBoundReason(EnrichStateCompleted, &EnrichResult{SymbolsTotal: 100, SymbolsCovered: 100}))
	assert.Equal(t, EnrichBoundCompletedAll, enrichBoundReason(EnrichStateCompleted, &EnrichResult{SymbolsTotal: 0}))
}

// A completed enrichment that covered only a sliver of its targets must surface
// the coverage figure and a "cap" bounding reason on the status — never a bare
// "completed" that reads as full enrichment.
func TestSetEnrichStatus_CoverageSurfaced(t *testing.T) {
	mgr := NewManager(Config{Enabled: true}, zap.NewNop())
	res := &EnrichResult{
		Provider:       "lsp-jdtls",
		Language:       "java",
		EdgesAdded:     156,
		EdgesConfirmed: 554,
		NodesEnriched:  35,
		SymbolsTotal:   1500,
		SymbolsCovered: 35,
	}
	res.CoveragePercent = float64(res.SymbolsCovered) / float64(res.SymbolsTotal) * 100
	mgr.setEnrichStatus("petclinic", "lsp-jdtls", "java", EnrichStateCompleted, 0, res, "")

	statuses := mgr.EnrichmentStatuses()
	var st *EnrichmentStatus
	for i := range statuses {
		if statuses[i].Provider == "lsp-jdtls" {
			st = &statuses[i]
		}
	}
	if assert.NotNil(t, st) {
		assert.Equal(t, EnrichStateCompleted, st.State)
		assert.Equal(t, 1500, st.SymbolsTotal)
		assert.Equal(t, 35, st.SymbolsCovered)
		assert.InDelta(t, 2.33, st.CoveragePercent, 0.1)
		assert.Equal(t, EnrichBoundCap, st.BoundReason,
			"a completed pass covering <100% of targets must report the cap bounding reason")
	}
	// The reason is back-stamped onto the result too.
	assert.Equal(t, EnrichBoundCap, res.BoundReason)
}
