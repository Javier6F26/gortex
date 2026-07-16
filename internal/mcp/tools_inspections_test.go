package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/contracts"
	"github.com/zzet/gortex/internal/graph"
)

func newInspectionsTestServer(t *testing.T) *Server {
	t.Helper()
	g := graph.New()

	// Dead code: unexported function with zero in-edges (exported
	// names are intentionally skipped by FindDeadCode — they're
	// assumed to be called from outside the indexed code).
	g.AddNode(&graph.Node{
		ID: "p/dead.go::dead", Name: "dead", Kind: graph.KindFunction,
		FilePath: "p/dead.go", StartLine: 4, EndLine: 6, Language: "go",
	})
	// Live function with one caller — not dead.
	g.AddNode(&graph.Node{
		ID: "p/live.go::live", Name: "live", Kind: graph.KindFunction,
		FilePath: "p/live.go", StartLine: 4, Language: "go",
	})
	g.AddNode(&graph.Node{
		ID: "p/live.go::caller", Name: "caller", Kind: graph.KindFunction,
		FilePath: "p/live.go", StartLine: 10, Language: "go",
	})
	g.AddEdge(&graph.Edge{From: "p/live.go::caller", To: "p/live.go::live", Kind: graph.EdgeCalls})

	// Coverage gap on live (40%).
	g.GetNode("p/live.go::live").Meta = map[string]any{"coverage_pct": 40.0}

	// TODO node.
	g.AddNode(&graph.Node{
		ID: "p/notes.go:42:todo", Kind: graph.KindTodo,
		FilePath: "p/notes.go", StartLine: 42,
		Meta: map[string]any{
			"tag":  "TODO",
			"text": "wire up retries",
		},
	})

	return &Server{
		graph:      g,
		session:    newSessionState(),
		tokenStats: &tokenStats{},
		symHistory: &symbolHistory{entries: make(map[string][]SymbolModification)},
		sessions:   newSessionMap(),
		toolScopes: newScopeRegistry(),
	}
}

func callListInspections(t *testing.T, s *Server, args map[string]any) map[string]any {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	res, err := s.handleListInspections(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.False(t, res.IsError)
	tc, ok := res.Content[0].(mcp.TextContent)
	require.True(t, ok)
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(tc.Text), &m))
	return m
}

func callRunInspections(t *testing.T, s *Server, args map[string]any) map[string]any {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	res, err := s.handleRunInspections(context.Background(), req)
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

func TestListInspections_ReturnsCatalog(t *testing.T) {
	s := newInspectionsTestServer(t)
	out := callListInspections(t, s, map[string]any{})

	rows, _ := out["inspections"].([]any)
	require.NotEmpty(t, rows)
	// Every entry must have id, category, description, severity.
	for _, r := range rows {
		m := r.(map[string]any)
		assert.NotEmpty(t, m["id"])
		assert.NotEmpty(t, m["category"])
		assert.NotEmpty(t, m["description"])
		assert.NotEmpty(t, m["severity"])
	}
}

func TestListInspections_CategoryFilter(t *testing.T) {
	s := newInspectionsTestServer(t)
	out := callListInspections(t, s, map[string]any{"category": "dead-code"})

	rows, _ := out["inspections"].([]any)
	require.NotEmpty(t, rows)
	for _, r := range rows {
		m := r.(map[string]any)
		assert.Equal(t, "dead-code", m["category"])
	}
}

func TestRunInspections_DeadCodeFires(t *testing.T) {
	s := newInspectionsTestServer(t)
	out := callRunInspections(t, s, map[string]any{
		"inspections": "dead_code",
	})

	results, _ := out["results"].([]any)
	require.NotEmpty(t, results)
	first := results[0].(map[string]any)
	assert.Equal(t, "dead_code", first["inspection"])
	violations, _ := first["violations"].([]any)
	require.NotEmpty(t, violations, "dead has zero in-edges, should be flagged")
	v := violations[0].(map[string]any)
	assert.Equal(t, "warning", v["severity"])
	assert.Equal(t, "p/dead.go::dead", v["symbol_id"])
}

func TestRunInspections_CoverageGapsFires(t *testing.T) {
	s := newInspectionsTestServer(t)
	out := callRunInspections(t, s, map[string]any{
		"inspections": "coverage_gaps",
	})

	results, _ := out["results"].([]any)
	require.Len(t, results, 1)
	violations, _ := results[0].(map[string]any)["violations"].([]any)
	require.NotEmpty(t, violations, "live has 40%% coverage — flagged")
	v := violations[0].(map[string]any)
	assert.Equal(t, "p/live.go::live", v["symbol_id"])
}

func TestRunInspections_TodosFire(t *testing.T) {
	s := newInspectionsTestServer(t)
	out := callRunInspections(t, s, map[string]any{
		"inspections": "todos",
	})

	results, _ := out["results"].([]any)
	require.Len(t, results, 1)
	violations, _ := results[0].(map[string]any)["violations"].([]any)
	require.Len(t, violations, 1)
	v := violations[0].(map[string]any)
	assert.Equal(t, "info", v["severity"])
	assert.Contains(t, v["message"].(string), "wire up retries")
}

func TestRunInspections_AllRunsEverything(t *testing.T) {
	s := newInspectionsTestServer(t)
	out := callRunInspections(t, s, map[string]any{"inspections": "all"})

	results, _ := out["results"].([]any)
	assert.GreaterOrEqual(t, len(results), 5, "every registry entry runs under inspections=all")
}

func TestRunInspections_SeverityFilter(t *testing.T) {
	s := newInspectionsTestServer(t)
	// info-only filter: dead_code (warning) drops out, todos (info) stays.
	out := callRunInspections(t, s, map[string]any{
		"inspections": "dead_code,todos",
		"severity":    "info",
	})

	results, _ := out["results"].([]any)
	totals := map[string]int{}
	for _, r := range results {
		m := r.(map[string]any)
		totals[m["inspection"].(string)] = int(m["total"].(float64))
	}
	assert.Zero(t, totals["dead_code"], "warning severity filtered out")
	assert.NotZero(t, totals["todos"], "info severity kept")
}

func TestRunInspections_MaxPerInspectionCap(t *testing.T) {
	s := newInspectionsTestServer(t)
	// Add 10 more todos.
	for i := range 10 {
		s.graph.AddNode(&graph.Node{
			ID: "p/extra.go::todo" + string(rune('A'+i)), Kind: graph.KindTodo,
			FilePath: "p/extra.go", StartLine: i + 1,
			Meta: map[string]any{"tag": "TODO", "text": "x"},
		})
	}

	out := callRunInspections(t, s, map[string]any{
		"inspections":        "todos",
		"max_per_inspection": 3,
	})
	results, _ := out["results"].([]any)
	r := results[0].(map[string]any)
	violations, _ := r["violations"].([]any)
	assert.LessOrEqual(t, len(violations), 3)
	assert.Equal(t, true, r["truncated"].(bool))
}

func TestRunInspections_PathPrefixScopes(t *testing.T) {
	s := newInspectionsTestServer(t)
	out := callRunInspections(t, s, map[string]any{
		"inspections": "dead_code",
		"path_prefix": "p/dead", // includes p/dead.go, excludes p/live.go
	})

	results, _ := out["results"].([]any)
	violations, _ := results[0].(map[string]any)["violations"].([]any)
	// Only `dead` from p/dead.go should be flagged; caller in p/live.go
	// (which also lacks callers and is unexported) must be excluded.
	for _, v := range violations {
		m := v.(map[string]any)
		assert.NotContains(t, m["file"].(string), "p/live.go",
			"path_prefix p/dead should exclude p/live.go nodes")
	}
}

func TestRunInspections_SummaryAggregates(t *testing.T) {
	s := newInspectionsTestServer(t)
	out := callRunInspections(t, s, map[string]any{"inspections": "dead_code,todos"})

	summary := out["summary"].(map[string]any)
	by := summary["by_inspection"].(map[string]any)
	total := int(summary["total_violations"].(float64))

	assert.GreaterOrEqual(t, total, 2)
	deadCount := int(by["dead_code"].(float64))
	todoCount := int(by["todos"].(float64))
	assert.Equal(t, deadCount+todoCount, total)
}

func TestRunInspections_ContractOrphansFire(t *testing.T) {
	s := newInspectionsTestServer(t)
	reg := contracts.NewRegistry()
	// Provider with no consumer.
	reg.Add(contracts.Contract{
		ID: "http:GET:/orphan", Type: contracts.ContractType("http"),
		Role: contracts.RoleProvider, FilePath: "p/handler.go", SymbolID: "p/handler.go::Orphan",
	})
	s.contractRegistry = reg

	out := callRunInspections(t, s, map[string]any{"inspections": "contracts_orphans"})
	results, _ := out["results"].([]any)
	violations, _ := results[0].(map[string]any)["violations"].([]any)
	require.Len(t, violations, 1)
	v := violations[0].(map[string]any)
	assert.Contains(t, v["message"].(string), "orphan contract")
}

// When the contract registry is unavailable (no contracts indexed and
// none persisted in the store — the follower-with-empty-store case), the
// inspection is reported skipped with a reason, NOT a silent
// zero-violation clean that reads as a real pass
// (fix-follower-contract-registry 2.1).
func TestRunInspections_ContractOrphansSkippedWhenRegistryAbsent(t *testing.T) {
	s := newInspectionsTestServer(t) // no contractRegistry, no contract nodes
	out := callRunInspections(t, s, map[string]any{"inspections": "contracts_orphans"})
	results, _ := out["results"].([]any)
	require.Len(t, results, 1)
	block := results[0].(map[string]any)
	assert.Equal(t, true, block["skipped"], "unavailable registry must mark the inspection skipped")
	assert.EqualValues(t, 0, block["total"])
	assert.Contains(t, block["reason"].(string), "registry unavailable")

	// It must not be counted as clean in the summary either.
	summary, _ := out["summary"].(map[string]any)
	byInspection, _ := summary["by_inspection"].(map[string]any)
	_, counted := byInspection["contracts_orphans"]
	assert.False(t, counted, "a skipped inspection must not appear in by_inspection as 0 violations")
}

func TestRunInspections_RejectsMissingInspectionsArg(t *testing.T) {
	s := newInspectionsTestServer(t)
	out := callRunInspections(t, s, map[string]any{})
	assert.True(t, out["is_error"] == true)
}

func TestRunInspections_UnknownInspectionsAreNoOps(t *testing.T) {
	s := newInspectionsTestServer(t)
	out := callRunInspections(t, s, map[string]any{"inspections": "definitely_not_a_real_inspector"})
	results, _ := out["results"].([]any)
	assert.Empty(t, results, "unknown ids don't error, they just produce no output")
}

func TestRunInspections_GuardsWhenRulesPresent(t *testing.T) {
	s := newInspectionsTestServer(t)
	s.guardRules = []config.GuardRule{{Name: "no-cross-package-state"}}

	out := callRunInspections(t, s, map[string]any{"inspections": "guards"})
	results, _ := out["results"].([]any)
	require.Len(t, results, 1)
	violations, _ := results[0].(map[string]any)["violations"].([]any)
	// At least one violation surfaces per scoped node.
	require.NotEmpty(t, violations)
	v := violations[0].(map[string]any)
	assert.Equal(t, "error", v["severity"])
	assert.Contains(t, v["message"].(string), "no-cross-package-state")
}
