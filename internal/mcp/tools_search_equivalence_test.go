package mcp

import (
	"context"
	"encoding/json"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/query"
	"github.com/zzet/gortex/internal/search"
)

// equivalenceTestServer builds a single-repo server with no LLM
// provider, so any expansion observed comes purely from the
// deterministic equivalence channel.
func equivalenceTestServer(t *testing.T, names []string, equivEnabled *bool) *Server {
	t.Helper()
	g := graph.New()
	bm := search.NewBM25()
	for i, n := range names {
		id := "pkg/" + n + ".go::" + n
		g.AddNode(&graph.Node{
			ID: id, Kind: graph.KindFunction, Name: n,
			FilePath: "pkg/" + n + ".go", StartLine: i + 1, EndLine: i + 5, Language: "go",
		})
		bm.Add(id, n, "pkg/"+n+".go", "")
	}
	eng := query.NewEngine(g)
	eng.SetSearch(bm)
	srv := NewServer(eng, g, nil, nil, zap.NewNop(), nil)
	srv.SetSearchConfig(config.SearchConfig{EquivalenceClasses: equivEnabled})
	srv.RunAnalysis() // builds the auto-concept vocabulary
	return srv
}

func searchIDs(t *testing.T, srv *Server, args map[string]any) map[string]bool {
	t.Helper()
	req := mcplib.CallToolRequest{}
	req.Params.Name = "search_symbols"
	req.Params.Arguments = args
	res, err := srv.handleSearchSymbols(context.Background(), req)
	require.NoError(t, err)
	require.Falsef(t, res.IsError, "search errored: %v", res.Content)
	var resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].(mcplib.TextContent).Text), &resp))
	ids := map[string]bool{}
	if results, ok := resp["results"].([]any); ok {
		for _, r := range results {
			ids[r.(map[string]any)["id"].(string)] = true
		}
	}
	return ids
}

// TestSearchSymbols_EquivalenceBridgesVocabulary is the core feature
// test: a query for "auth" must reach a LoginService symbol via the
// curated equivalence class -- with NO LLM provider configured.
func TestSearchSymbols_EquivalenceBridgesVocabulary(t *testing.T) {
	srv := equivalenceTestServer(t, []string{
		"LoginService", "SigninController", "ParseConfig", "UnrelatedThing",
	}, nil) // nil EquivalenceClasses == enabled by default

	ids := searchIDs(t, srv, map[string]any{"query": "auth"})
	require.Truef(t, ids["pkg/LoginService.go::LoginService"],
		"equivalence expansion should bridge 'auth' to LoginService; got %v", ids)
	require.Truef(t, ids["pkg/SigninController.go::SigninController"],
		"equivalence expansion should bridge 'auth' to SigninController; got %v", ids)
}

// TestSearchSymbols_EquivalenceDeleteRemove confirms the delete/remove
// bridge works in both directions through the BM25 OR-merge.
func TestSearchSymbols_EquivalenceDeleteRemove(t *testing.T) {
	srv := equivalenceTestServer(t, []string{
		"RemoveUser", "DropTable", "FetchProfile",
	}, nil)
	ids := searchIDs(t, srv, map[string]any{"query": "delete"})
	require.Truef(t, ids["pkg/RemoveUser.go::RemoveUser"],
		"'delete' should bridge to RemoveUser; got %v", ids)
	require.Truef(t, ids["pkg/DropTable.go::DropTable"],
		"'delete' should bridge to DropTable; got %v", ids)
}

// TestSearchSymbols_ExpandOffDisablesEquivalence confirms expand:"off"
// suppresses the equivalence channel -- "auth" then matches nothing.
func TestSearchSymbols_ExpandOffDisablesEquivalence(t *testing.T) {
	srv := equivalenceTestServer(t, []string{"LoginService", "SigninController"}, nil)
	ids := searchIDs(t, srv, map[string]any{"query": "auth", "expand": "off"})
	require.Falsef(t, ids["pkg/LoginService.go::LoginService"],
		"expand:off must not bridge 'auth' to LoginService; got %v", ids)
}

// TestSearchSymbols_EquivalenceDisabledViaConfig confirms the config
// toggle (equivalence_classes:false) disables the channel.
func TestSearchSymbols_EquivalenceDisabledViaConfig(t *testing.T) {
	off := false
	srv := equivalenceTestServer(t, []string{"LoginService"}, &off)
	ids := searchIDs(t, srv, map[string]any{"query": "auth"})
	require.Falsef(t, ids["pkg/LoginService.go::LoginService"],
		"equivalence_classes:false must disable the channel; got %v", ids)
}

// TestSearchSymbols_EquivalenceAutoConcepts confirms the per-repo
// auto-mined concept vocabulary bridges a domain phrase: a query for
// "blast" reaches a symbol named only with "radius" because the two
// words co-occur across the repo's symbol names.
func TestSearchSymbols_EquivalenceAutoConcepts(t *testing.T) {
	srv := equivalenceTestServer(t, []string{
		"handleBlastRadius", "blastRadiusOf", "BlastRadiusReport",
		"computeBlastRadius", "radiusOnlySymbol",
	}, nil)
	ids := searchIDs(t, srv, map[string]any{"query": "blast"})
	require.Truef(t, ids["pkg/radiusOnlySymbol.go::radiusOnlySymbol"],
		"auto-concept mining should bridge 'blast' to a radius-named symbol; got %v", ids)
}

// TestSearchSymbols_ConceptThesaurusBridge confirms the concept-
// relatedness thesaurus bridges an adjacent (non-synonym) concept
// under expand:"equivalence" with NO LLM provider: a query for "auth"
// reaches a symbol named only with "token".
func TestSearchSymbols_ConceptThesaurusBridge(t *testing.T) {
	srv := equivalenceTestServer(t, []string{
		"LoginService", "TokenStore", "UnrelatedThing",
	}, nil)
	ids := searchIDs(t, srv, map[string]any{"query": "auth", "expand": "equivalence"})
	// Direct synonym bridge still works.
	require.Truef(t, ids["pkg/LoginService.go::LoginService"],
		"synonym bridge auth -> LoginService should still hold; got %v", ids)
	// Thesaurus (adjacent-concept) bridge: auth relates to the token
	// class, so TokenStore is reachable even though "token" is not a
	// synonym of "auth".
	require.Truef(t, ids["pkg/TokenStore.go::TokenStore"],
		"thesaurus bridge auth -> token -> TokenStore should hold; got %v", ids)
}
