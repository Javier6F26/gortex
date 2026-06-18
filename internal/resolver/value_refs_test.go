package resolver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

func valueRefCandidate(g graph.Store, from, name, file string, line int) {
	g.AddEdge(&graph.Edge{
		From: from, To: "unresolved::valueref::" + name, Kind: graph.EdgeReads,
		FilePath: file, Line: line, Origin: graph.OriginSpeculative,
		Meta: map[string]any{"via": valueRefCandidateVia, "name": name},
	})
}

func readsEdge(g graph.Store, from, to string) *graph.Edge {
	for _, e := range g.GetInEdges(to) {
		if e.From == from && e.Kind == graph.EdgeReads && e.Meta != nil {
			if v, _ := e.Meta["via"].(string); v == valueRefVia {
				return e
			}
		}
	}
	return nil
}

// TestValueRefConstReaderImpactRadius is the C2 named test: a function that
// reads a distinctive same-file constant gains a tiered EdgeReads to it, so the
// reader appears in the constant's impact radius (incoming non-Defines/MemberOf
// edges) — which blast-radius analysis walks.
func TestValueRefConstReaderImpactRadius(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{
		ID: "cfg.go::MAX_RETRIES", Kind: graph.KindConstant, Name: "MAX_RETRIES",
		FilePath: "cfg.go", StartLine: 3, Language: "go",
	})
	g.AddNode(&graph.Node{
		ID: "cfg.go::DoWork", Kind: graph.KindFunction, Name: "DoWork",
		FilePath: "cfg.go", StartLine: 10, Language: "go",
	})
	g.AddNode(&graph.Node{
		ID: "cfg.go::lower", Kind: graph.KindConstant, Name: "ab",
		FilePath: "cfg.go", StartLine: 4, Language: "go",
	})
	valueRefCandidate(g, "cfg.go::DoWork", "MAX_RETRIES", "cfg.go", 12)
	// A short / non-distinctive name must NOT bind.
	valueRefCandidate(g, "cfg.go::DoWork", "ab", "cfg.go", 13)

	n := ResolveValueRefs(g)
	assert.Equal(t, 1, n, "only the distinctive constant read binds")

	e := readsEdge(g, "cfg.go::DoWork", "cfg.go::MAX_RETRIES")
	require.NotNil(t, e, "reader should gain a value-ref EdgeReads to the constant")
	assert.Equal(t, graph.OriginASTResolved, e.Origin, "the read must ride a provenance tier")

	// Impact-radius property: the reader is among the constant's incoming
	// (non-Defines/MemberOf) edges, which fillImpactLive walks.
	var inRadius bool
	for _, in := range g.GetInEdges("cfg.go::MAX_RETRIES") {
		if in.From == "cfg.go::DoWork" && in.Kind != graph.EdgeDefines && in.Kind != graph.EdgeMemberOf {
			inRadius = true
		}
	}
	assert.True(t, inRadius, "DoWork must appear in MAX_RETRIES' impact radius")
}

// TestValueRefShadowAndSelfPruned confirms a same-file parameter shadows the
// constant (no bind) and a constant never reads itself.
func TestValueRefShadowAndSelfPruned(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{
		ID: "f.go::TIMEOUT", Kind: graph.KindConstant, Name: "TIMEOUT",
		FilePath: "f.go", StartLine: 2, Language: "go",
	})
	g.AddNode(&graph.Node{
		ID: "f.go::Run", Kind: graph.KindFunction, Name: "Run", FilePath: "f.go", StartLine: 5, Language: "go",
	})
	g.AddNode(&graph.Node{
		ID: "f.go::Run.TIMEOUT", Kind: graph.KindParam, Name: "TIMEOUT", FilePath: "f.go", StartLine: 5, Language: "go",
	})
	valueRefCandidate(g, "f.go::Run", "TIMEOUT", "f.go", 6)   // shadowed by the param
	valueRefCandidate(g, "f.go::TIMEOUT", "TIMEOUT", "f.go", 2) // self-read

	assert.Equal(t, 0, ResolveValueRefs(g), "shadowed and self reads must be pruned")
}
