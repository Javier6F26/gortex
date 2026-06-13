package analysis

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/query"
)

// buildReturnUsageGraph wires a target function with three classified
// call sites and one the extractor left unstamped.
func buildReturnUsageGraph(t *testing.T) (*graph.Graph, string) {
	t.Helper()
	g := graph.New()
	targetID := "pkg/t.go::Target"
	g.AddNode(&graph.Node{
		ID: targetID, Kind: graph.KindFunction, Name: "Target",
		FilePath: "pkg/t.go", StartLine: 1,
		Meta: map[string]any{"signature": "func() error"},
	})
	for i, usage := range []string{
		graph.ReturnUsageDiscarded,
		graph.ReturnUsageDiscarded,
		graph.ReturnUsageAssigned,
		"", // unclassified site
	} {
		callerID := "pkg/c.go::caller" + string(rune('A'+i))
		g.AddNode(&graph.Node{
			ID: callerID, Kind: graph.KindFunction, Name: "caller",
			FilePath: "pkg/c.go", StartLine: 10 * (i + 1),
		})
		e := &graph.Edge{
			From: callerID, To: targetID, Kind: graph.EdgeCalls,
			FilePath: "pkg/c.go", Line: 10*(i+1) + 1,
		}
		if usage != "" {
			e.Meta = map[string]any{graph.MetaReturnUsage: usage}
		}
		g.AddEdge(e)
	}
	return g, targetID
}

func TestVerifyChanges_ReturnUsageSummary(t *testing.T) {
	g, targetID := buildReturnUsageGraph(t)
	engine := query.NewEngine(g)

	result := VerifyChanges(g, engine, []SignatureChange{
		{SymbolID: targetID, NewSignature: "func() (int, error)"},
	})

	require.Len(t, result.ReturnUsage, 1)
	ru := result.ReturnUsage[0]
	assert.Equal(t, targetID, ru.SymbolID)
	assert.Equal(t, 4, ru.CallSites)
	assert.Equal(t, 2, ru.Counts[graph.ReturnUsageDiscarded])
	assert.Equal(t, 1, ru.Counts[graph.ReturnUsageAssigned])
	assert.Equal(t, 1, ru.Unclassified)
}

// Speculative dispatch edges are hidden by default on every read
// surface (find_usages and the rest), so the return-usage distribution
// must not count them either — otherwise verify_change disagrees with
// the call sites a user actually sees.
func TestVerifyChanges_ReturnUsageSkipsSpeculative(t *testing.T) {
	g := graph.New()
	targetID := "pkg/t.go::Target"
	g.AddNode(&graph.Node{
		ID: targetID, Kind: graph.KindFunction, Name: "Target",
		FilePath: "pkg/t.go", StartLine: 1,
		Meta: map[string]any{"signature": "func() error"},
	})
	// One concrete (visible) assigned call site.
	g.AddNode(&graph.Node{
		ID: "pkg/c.go::real", Kind: graph.KindFunction, Name: "real", FilePath: "pkg/c.go", StartLine: 10,
	})
	g.AddEdge(&graph.Edge{
		From: "pkg/c.go::real", To: targetID, Kind: graph.EdgeCalls,
		FilePath: "pkg/c.go", Line: 11,
		Meta:     map[string]any{graph.MetaReturnUsage: graph.ReturnUsageAssigned},
	})
	// One speculative dispatch call site — hidden from read surfaces by
	// default, so it must not appear in the distribution.
	g.AddNode(&graph.Node{
		ID: "pkg/c.go::spec", Kind: graph.KindFunction, Name: "spec", FilePath: "pkg/c.go", StartLine: 20,
	})
	g.AddEdge(&graph.Edge{
		From: "pkg/c.go::spec", To: targetID, Kind: graph.EdgeCalls,
		FilePath: "pkg/c.go", Line: 21, Origin: graph.OriginSpeculative,
		Meta: map[string]any{
			graph.MetaReturnUsage:  graph.ReturnUsageDiscarded,
			graph.MetaSpeculative:  true,
		},
	})
	engine := query.NewEngine(g)

	result := VerifyChanges(g, engine, []SignatureChange{
		{SymbolID: targetID, NewSignature: "func() (int, error)"},
	})

	require.Len(t, result.ReturnUsage, 1)
	ru := result.ReturnUsage[0]
	assert.Equal(t, 1, ru.CallSites, "speculative call site must not be counted")
	assert.Equal(t, 1, ru.Counts[graph.ReturnUsageAssigned])
	assert.Zero(t, ru.Counts[graph.ReturnUsageDiscarded], "speculative discarded site must be excluded")
	assert.Zero(t, ru.Unclassified)
}

// A non-callable symbol must not produce a distribution: return-usage
// only means something for function/method return values.
func TestVerifyChanges_ReturnUsageSkipsNonFunctions(t *testing.T) {
	g := graph.New()
	typeID := "pkg/t.go::Config"
	g.AddNode(&graph.Node{
		ID: typeID, Kind: graph.KindType, Name: "Config", FilePath: "pkg/t.go",
	})
	g.AddNode(&graph.Node{
		ID: "pkg/c.go::user", Kind: graph.KindFunction, Name: "user", FilePath: "pkg/c.go",
	})
	g.AddEdge(&graph.Edge{
		From: "pkg/c.go::user", To: typeID, Kind: graph.EdgeReferences,
		FilePath: "pkg/c.go", Line: 4,
	})
	engine := query.NewEngine(g)

	result := VerifyChanges(g, engine, []SignatureChange{
		{SymbolID: typeID, NewSignature: "struct{}"},
	})
	assert.Empty(t, result.ReturnUsage)
}

// A function nobody calls reports no distribution rather than an empty
// one — there is nothing to break.
func TestVerifyChanges_ReturnUsageSkipsUncalled(t *testing.T) {
	g := graph.New()
	fnID := "pkg/t.go::Lonely"
	g.AddNode(&graph.Node{
		ID: fnID, Kind: graph.KindFunction, Name: "Lonely", FilePath: "pkg/t.go",
		Meta: map[string]any{"signature": "func()"},
	})
	engine := query.NewEngine(g)

	result := VerifyChanges(g, engine, []SignatureChange{
		{SymbolID: fnID, NewSignature: "func() error"},
	})
	assert.Empty(t, result.ReturnUsage)
}
