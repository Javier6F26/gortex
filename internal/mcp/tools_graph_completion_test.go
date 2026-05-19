package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

func newGraphCompletionTestServer(t *testing.T) *Server {
	t.Helper()
	g := graph.New()

	g.AddNode(&graph.Node{ID: "p/auth.go::Login", Name: "Login", Kind: graph.KindFunction, FilePath: "p/auth.go"})
	g.AddNode(&graph.Node{ID: "p/auth.go::checkPassword", Name: "checkPassword", Kind: graph.KindFunction, FilePath: "p/auth.go"})
	g.AddNode(&graph.Node{ID: "p/auth.go::issueToken", Name: "issueToken", Kind: graph.KindFunction, FilePath: "p/auth.go"})
	g.AddNode(&graph.Node{ID: "p/unrelated.go::Foo", Name: "Foo", Kind: graph.KindFunction, FilePath: "p/unrelated.go"})

	g.AddEdge(&graph.Edge{From: "p/auth.go::Login", To: "p/auth.go::checkPassword", Kind: graph.EdgeCalls})
	g.AddEdge(&graph.Edge{From: "p/auth.go::Login", To: "p/auth.go::issueToken", Kind: graph.EdgeCalls})

	return &Server{
		graph:      g,
		session:    newSessionState(),
		tokenStats: &tokenStats{},
		symHistory: &symbolHistory{entries: make(map[string][]SymbolModification)},
		sessions:   newSessionMap(),
		toolScopes: newScopeRegistry(),
	}
}

func callGraphCompletion(t *testing.T, s *Server, args map[string]any) map[string]any {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	res, err := s.handleGraphCompletionSearch(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, res)
	if res.IsError {
		return map[string]any{"is_error": true}
	}
	tc, ok := res.Content[0].(mcp.TextContent)
	require.True(t, ok)
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(tc.Text), &m))
	return m
}

func TestGraphCompletionSearch_SeedAndExpand(t *testing.T) {
	s := newGraphCompletionTestServer(t)
	out := callGraphCompletion(t, s, map[string]any{"query": "Login"})

	results, _ := out["results"].([]any)
	require.GreaterOrEqual(t, len(results), 3, "Login seeded + 2 callees expanded")
	ids := map[string]bool{}
	for _, r := range results {
		ids[r.(map[string]any)["id"].(string)] = true
	}
	assert.True(t, ids["p/auth.go::Login"])
	assert.True(t, ids["p/auth.go::checkPassword"])
	assert.True(t, ids["p/auth.go::issueToken"])
	assert.False(t, ids["p/unrelated.go::Foo"], "unrelated node not reached via 1-hop expansion")
}

func TestGraphCompletionSearch_IsSeedFlag(t *testing.T) {
	s := newGraphCompletionTestServer(t)
	out := callGraphCompletion(t, s, map[string]any{"query": "Login"})

	results, _ := out["results"].([]any)
	seeds := 0
	expanded := 0
	for _, r := range results {
		m := r.(map[string]any)
		if m["is_seed"].(bool) {
			seeds++
		} else {
			expanded++
		}
	}
	assert.EqualValues(t, seeds, out["seed_count"].(float64))
	assert.EqualValues(t, expanded, out["expanded"].(float64))
}

func TestGraphCompletionSearch_RetrieverNameEchoed(t *testing.T) {
	s := newGraphCompletionTestServer(t)
	out := callGraphCompletion(t, s, map[string]any{"query": "Login"})
	assert.Equal(t, "graph_completion", out["retriever"])
}

func TestGraphCompletionSearch_EdgeKindsAll(t *testing.T) {
	s := newGraphCompletionTestServer(t)
	// Add a references edge from Login → Foo to test that 'all' picks it up.
	s.graph.AddEdge(&graph.Edge{From: "p/auth.go::Login", To: "p/unrelated.go::Foo", Kind: graph.EdgeReferences})

	out := callGraphCompletion(t, s, map[string]any{"query": "Login", "edge_kinds": "all"})
	results, _ := out["results"].([]any)
	ids := map[string]bool{}
	for _, r := range results {
		ids[r.(map[string]any)["id"].(string)] = true
	}
	assert.True(t, ids["p/unrelated.go::Foo"], "Foo reached when all edge kinds traversed")
}

func TestGraphCompletionSearch_EdgeKindsRestrict(t *testing.T) {
	s := newGraphCompletionTestServer(t)
	s.graph.AddEdge(&graph.Edge{From: "p/auth.go::Login", To: "p/unrelated.go::Foo", Kind: graph.EdgeReferences})

	out := callGraphCompletion(t, s, map[string]any{
		"query":      "Login",
		"edge_kinds": "calls", // exclude references
	})
	results, _ := out["results"].([]any)
	ids := map[string]bool{}
	for _, r := range results {
		ids[r.(map[string]any)["id"].(string)] = true
	}
	assert.False(t, ids["p/unrelated.go::Foo"], "Foo reachable only via References — filtered out")
	assert.True(t, ids["p/auth.go::checkPassword"], "calls edge target kept")
}

func TestGraphCompletionSearch_RejectsEmptyQuery(t *testing.T) {
	s := newGraphCompletionTestServer(t)
	out := callGraphCompletion(t, s, map[string]any{})
	assert.True(t, out["is_error"] == true)
}

func TestGraphCompletionSearch_LimitHonored(t *testing.T) {
	s := newGraphCompletionTestServer(t)
	out := callGraphCompletion(t, s, map[string]any{"query": "Login", "limit": 1})
	results, _ := out["results"].([]any)
	assert.Len(t, results, 1)
}

func TestNameMatchSeeder_CaseInsensitive(t *testing.T) {
	s := newGraphCompletionTestServer(t)
	out, err := s.nameMatchSeeder(context.Background(), s.graph, "login", 10)
	require.NoError(t, err)
	require.NotEmpty(t, out)
	assert.Equal(t, "Login", out[0].Node.Name)
}
