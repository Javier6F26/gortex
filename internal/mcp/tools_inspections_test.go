package mcp

import (
	"context"
	"encoding/json"
	"strings"
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

// seedTodos adds n TODO nodes so an inspection can produce an arbitrary
// violation count for cap / budget regression testing.
func seedTodos(s *Server, n int) {
	for i := range n {
		s.graph.AddNode(&graph.Node{
			ID:       "p/bulk.go::todo" + itoa(i),
			Kind:     graph.KindTodo,
			FilePath: "p/bulk.go", StartLine: i + 1,
			Meta: map[string]any{"tag": "TODO", "text": "bulk todo with some padding text to grow the payload"},
		})
	}
}

// blockByID returns the result block for inspection id (nil if absent).
func blockByID(out map[string]any, id string) map[string]any {
	results, _ := out["results"].([]any)
	for _, r := range results {
		m, _ := r.(map[string]any)
		if m["inspection"] == id {
			return m
		}
	}
	return nil
}

// TestRunInspections_SummaryMatchesBlocksAcrossCaps is the fix-contracts-
// hydration-residuals 2.2 regression: the summary is built from the emitted
// blocks, so `total_violations` always equals the sum of the present blocks'
// `total` and the block is never silently dropped — at cap<total, cap==total,
// and cap>total.
func TestRunInspections_SummaryMatchesBlocksAcrossCaps(t *testing.T) {
	const todoCount = 120
	for _, cap := range []int{50, todoCount, 500} {
		s := newInspectionsTestServer(t)
		seedTodos(s, todoCount)
		out := callRunInspections(t, s, map[string]any{
			"inspections":        "todos",
			"max_per_inspection": cap,
		})

		block := blockByID(out, "todos")
		require.NotNil(t, block, "cap=%d: todos block must be present, never elided", cap)
		total := int(block["total"].(float64))
		returned := int(block["returned"].(float64))
		violations, _ := block["violations"].([]any)
		truncated := block["truncated"].(bool)

		require.GreaterOrEqual(t, total, todoCount, "cap=%d: true total must count every todo", cap)
		assert.Equal(t, returned, len(violations), "cap=%d: returned must equal rows carried", cap)

		want := min(cap, total)
		assert.Equal(t, want, returned, "cap=%d: returned must be min(cap,total)", cap)
		assert.Equal(t, total > returned, truncated, "cap=%d: truncated iff rows were dropped", cap)

		// Summary must reconcile with the block.
		summary := out["summary"].(map[string]any)
		by := summary["by_inspection"].(map[string]any)
		assert.Equal(t, total, int(by["todos"].(float64)), "cap=%d: summary by_inspection == block total", cap)
		assert.Equal(t, total, int(summary["total_violations"].(float64)),
			"cap=%d: summary.total_violations must never count violations no block carries", cap)

		if cap >= total {
			assert.False(t, truncated, "cap>=total: block must carry all violations")
			assert.Equal(t, total, returned, "cap>=total: all violations returned")
		}
	}
}

// TestRunInspections_ExplicitBudgetKeepsBlock is the other half of 2.2: an
// explicit max_bytes small enough to force trimming must shrink the block's
// violation array WITHOUT dropping the block, keep the true `total`, mark it
// truncated, and leave the summary reconciled.
func TestRunInspections_ExplicitBudgetKeepsBlock(t *testing.T) {
	s := newInspectionsTestServer(t)
	seedTodos(s, 300)
	out := callRunInspections(t, s, map[string]any{
		"inspections":        "todos",
		"max_per_inspection": 300,
		"max_bytes":          4000,
	})

	block := blockByID(out, "todos")
	require.NotNil(t, block, "block must survive an explicit byte budget, never be elided")
	total := int(block["total"].(float64))
	returned := int(block["returned"].(float64))
	violations, _ := block["violations"].([]any)

	assert.GreaterOrEqual(t, total, 300, "true total preserved under budget")
	assert.Less(t, returned, total, "an explicit budget must have trimmed rows")
	assert.Equal(t, returned, len(violations))
	assert.True(t, block["truncated"].(bool), "trimmed block must be marked truncated")

	summary := out["summary"].(map[string]any)
	assert.Equal(t, total, int(summary["total_violations"].(float64)),
		"summary must count the true total, reconciled with the truncation-marked block")
	assert.Equal(t, returned, int(summary["returned_violations"].(float64)))
}

// TestRunInspections_ContractOrphansMatchedPairNotFlagged is the 3.2
// regression: when a provider and a consumer share a contract ID, the pair
// is matched and neither is reported as an orphan; a lone provider still is.
func TestRunInspections_ContractOrphansMatchedPairNotFlagged(t *testing.T) {
	s := newInspectionsTestServer(t)
	reg := contracts.NewRegistry()
	// Matched pair — same contract ID, both roles present.
	reg.Add(contracts.Contract{
		ID: "http:GET:/paired", Type: contracts.ContractType("http"),
		Role: contracts.RoleProvider, FilePath: "p/server.go", SymbolID: "p/server.go::Serve",
	})
	reg.Add(contracts.Contract{
		ID: "http:GET:/paired", Type: contracts.ContractType("http"),
		Role: contracts.RoleConsumer, FilePath: "p/client.go", SymbolID: "p/client.go::Call",
	})
	// Lone provider — genuine orphan.
	reg.Add(contracts.Contract{
		ID: "http:GET:/lonely", Type: contracts.ContractType("http"),
		Role: contracts.RoleProvider, FilePath: "p/server.go", SymbolID: "p/server.go::Lonely",
	})
	s.contractRegistry = reg

	out := callRunInspections(t, s, map[string]any{"inspections": "contracts_orphans"})
	block := blockByID(out, "contracts_orphans")
	require.NotNil(t, block)
	violations, _ := block["violations"].([]any)

	for _, v := range violations {
		m := v.(map[string]any)
		assert.NotContains(t, m["message"].(string), "/paired",
			"a matched provider/consumer pair must not be flagged as an orphan")
	}
	// The lone provider must still be flagged.
	var sawLonely bool
	for _, v := range violations {
		m := v.(map[string]any)
		if strings.Contains(m["message"].(string), "/lonely") {
			sawLonely = true
		}
	}
	assert.True(t, sawLonely, "a provider with no consumer must still be flagged")
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
