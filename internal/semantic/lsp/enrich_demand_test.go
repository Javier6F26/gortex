package lsp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zzet/gortex/internal/graph"
)

// The add-phase prioritises declarations that still carry unresolved same-name
// call candidates, so a deadline-bound pass spends its budget where an LSP
// references sweep can actually bind dropped call sites.
func TestEnrichNodeHasUnresolvedDemand(t *testing.T) {
	g := graph.New()
	demanded := &graph.Node{ID: "Repo.java::OwnerRepository.findByLastNameStartingWith", Kind: graph.KindMethod, Name: "findByLastNameStartingWith", FilePath: "Repo.java", Language: "java"}
	covered := &graph.Node{ID: "Repo.java::OwnerRepository.findById", Kind: graph.KindMethod, Name: "findById", FilePath: "Repo.java", Language: "java"}
	g.AddNode(demanded)
	g.AddNode(covered)
	g.AddNode(&graph.Node{ID: "T.java::T.run", Kind: graph.KindMethod, Name: "run", FilePath: "T.java", Language: "java"})

	// An unresolved same-name candidate names findByLastNameStartingWith.
	g.AddEdge(&graph.Edge{From: "T.java::T.run", To: "unresolved::*.findByLastNameStartingWith", Kind: graph.EdgeCalls, FilePath: "T.java", Line: 94})
	// findById is already covered — a resolved incoming call, no unresolved stub.
	g.AddEdge(&graph.Edge{From: "T.java::T.run", To: covered.ID, Kind: graph.EdgeCalls, FilePath: "T.java", Line: 97})

	assert.True(t, enrichNodeHasUnresolvedDemand(g, demanded),
		"a declaration with unresolved same-name candidates has enrichment demand")
	assert.False(t, enrichNodeHasUnresolvedDemand(g, covered),
		"a fully-resolved declaration has no enrichment demand")
	// A non-callable node never has demand.
	assert.False(t, enrichNodeHasUnresolvedDemand(g, &graph.Node{ID: "x", Kind: graph.KindType, Name: "findByLastNameStartingWith"}))
}
