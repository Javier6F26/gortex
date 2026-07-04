package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

func newExtractTestServer(t *testing.T) *Server {
	t.Helper()
	g := graph.New()

	// orchestrator: 50 lines, 5 callers, 10 distinct callees — prime candidate.
	g.AddNode(&graph.Node{
		ID: "p/big.go::Orchestrator", Name: "Orchestrator", Kind: graph.KindFunction,
		FilePath: "p/big.go", StartLine: 1, EndLine: 50,
	})
	// 5 callers of Orchestrator.
	for i := 1; i <= 5; i++ {
		callerID := fmt.Sprintf("p/caller%d.go::Call%d", i, i)
		g.AddNode(&graph.Node{ID: callerID, Name: fmt.Sprintf("Call%d", i), Kind: graph.KindFunction, FilePath: fmt.Sprintf("p/caller%d.go", i)})
		g.AddEdge(&graph.Edge{From: callerID, To: "p/big.go::Orchestrator", Kind: graph.EdgeCalls})
	}
	// 10 distinct callees from Orchestrator.
	for i := 1; i <= 10; i++ {
		calleeID := fmt.Sprintf("p/util%d.go::U%d", i, i)
		g.AddNode(&graph.Node{ID: calleeID, Name: fmt.Sprintf("U%d", i), Kind: graph.KindFunction, FilePath: fmt.Sprintf("p/util%d.go", i)})
		g.AddEdge(&graph.Edge{From: "p/big.go::Orchestrator", To: calleeID, Kind: graph.EdgeCalls})
	}

	// short: 10 lines, 5 callers, 2 callees — fails min_lines.
	g.AddNode(&graph.Node{
		ID: "p/small.go::Short", Name: "Short", Kind: graph.KindFunction,
		FilePath: "p/small.go", StartLine: 1, EndLine: 10,
	})
	for i := 1; i <= 5; i++ {
		callerID := fmt.Sprintf("p/sc%d.go::SC%d", i, i)
		g.AddNode(&graph.Node{ID: callerID, Name: fmt.Sprintf("SC%d", i), Kind: graph.KindFunction, FilePath: fmt.Sprintf("p/sc%d.go", i)})
		g.AddEdge(&graph.Edge{From: callerID, To: "p/small.go::Short", Kind: graph.EdgeCalls})
	}

	// solo: 50 lines, 1 caller, 10 callees — fails min_callers.
	g.AddNode(&graph.Node{
		ID: "p/solo.go::Solo", Name: "Solo", Kind: graph.KindFunction,
		FilePath: "p/solo.go", StartLine: 1, EndLine: 50,
	})
	g.AddNode(&graph.Node{ID: "p/solocaller.go::SoloCaller", Name: "SoloCaller", Kind: graph.KindFunction, FilePath: "p/solocaller.go"})
	g.AddEdge(&graph.Edge{From: "p/solocaller.go::SoloCaller", To: "p/solo.go::Solo", Kind: graph.EdgeCalls})

	// flat: 50 lines, 5 callers, 1 callee — fails min_fan_out.
	g.AddNode(&graph.Node{
		ID: "p/flat.go::Flat", Name: "Flat", Kind: graph.KindFunction,
		FilePath: "p/flat.go", StartLine: 1, EndLine: 50,
	})
	for i := 1; i <= 5; i++ {
		callerID := fmt.Sprintf("p/fc%d.go::FC%d", i, i)
		g.AddNode(&graph.Node{ID: callerID, Name: fmt.Sprintf("FC%d", i), Kind: graph.KindFunction, FilePath: fmt.Sprintf("p/fc%d.go", i)})
		g.AddEdge(&graph.Edge{From: callerID, To: "p/flat.go::Flat", Kind: graph.EdgeCalls})
	}
	g.AddNode(&graph.Node{ID: "p/flatcallee.go::FCU", Name: "FCU", Kind: graph.KindFunction, FilePath: "p/flatcallee.go"})
	g.AddEdge(&graph.Edge{From: "p/flat.go::Flat", To: "p/flatcallee.go::FCU", Kind: graph.EdgeCalls})

	return &Server{
		graph:      g,
		session:    newSessionState(),
		tokenStats: &tokenStats{},
		symHistory: &symbolHistory{entries: make(map[string][]SymbolModification)},
		sessions:   newSessionMap(),
		toolScopes: newScopeRegistry(),
	}
}

func callExtractHandler(t *testing.T, s *Server, args map[string]any) map[string]any {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	res, err := s.handleGetExtractionCandidates(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.False(t, res.IsError)
	tc, ok := res.Content[0].(mcp.TextContent)
	require.True(t, ok)
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(tc.Text), &m))
	return m
}

func TestExtractCandidates_OrchestratorSurfaces(t *testing.T) {
	s := newExtractTestServer(t)
	out := callExtractHandler(t, s, map[string]any{})

	cands, _ := out["candidates"].([]any)
	require.NotEmpty(t, cands)
	first := cands[0].(map[string]any)
	assert.Equal(t, "p/big.go::Orchestrator", first["symbol_id"],
		"the 50-line / 5-caller / 10-callee function should rank highest")
	assert.EqualValues(t, 50, first["line_count"].(float64))
	assert.EqualValues(t, 5, first["caller_count"].(float64))
	assert.EqualValues(t, 10, first["fan_out"].(float64))
}

func TestExtractCandidates_FailsMinLines(t *testing.T) {
	s := newExtractTestServer(t)
	out := callExtractHandler(t, s, map[string]any{})

	cands, _ := out["candidates"].([]any)
	for _, c := range cands {
		assert.NotEqual(t, "p/small.go::Short", c.(map[string]any)["symbol_id"],
			"Short fails min_lines=20")
	}
}

func TestExtractCandidates_FailsMinCallers(t *testing.T) {
	s := newExtractTestServer(t)
	out := callExtractHandler(t, s, map[string]any{})

	cands, _ := out["candidates"].([]any)
	for _, c := range cands {
		assert.NotEqual(t, "p/solo.go::Solo", c.(map[string]any)["symbol_id"],
			"Solo fails min_callers=2")
	}
}

func TestExtractCandidates_FailsMinFanOut(t *testing.T) {
	s := newExtractTestServer(t)
	out := callExtractHandler(t, s, map[string]any{})

	cands, _ := out["candidates"].([]any)
	for _, c := range cands {
		assert.NotEqual(t, "p/flat.go::Flat", c.(map[string]any)["symbol_id"],
			"Flat fails min_fan_out=5")
	}
}

func TestExtractCandidates_RelaxedThresholdsSurface(t *testing.T) {
	s := newExtractTestServer(t)
	// Drop all thresholds to 0 — Solo and Flat should now appear.
	out := callExtractHandler(t, s, map[string]any{
		"min_lines":   1,
		"min_callers": 0,
		"min_fan_out": 0,
	})
	cands, _ := out["candidates"].([]any)
	ids := map[string]bool{}
	for _, c := range cands {
		ids[c.(map[string]any)["symbol_id"].(string)] = true
	}
	assert.True(t, ids["p/solo.go::Solo"])
	assert.True(t, ids["p/flat.go::Flat"])
}

func TestExtractCandidates_RationaleMentionsSignals(t *testing.T) {
	s := newExtractTestServer(t)
	out := callExtractHandler(t, s, map[string]any{})

	cands, _ := out["candidates"].([]any)
	require.NotEmpty(t, cands)
	rationale := cands[0].(map[string]any)["rationale"].(string)
	assert.Contains(t, rationale, "lines", "rationale names line count signal")
}

func TestExtractCandidates_PathPrefixScope(t *testing.T) {
	s := newExtractTestServer(t)
	out := callExtractHandler(t, s, map[string]any{"path_prefix": "p/big"})

	cands, _ := out["candidates"].([]any)
	require.Len(t, cands, 1)
	assert.Equal(t, "p/big.go::Orchestrator", cands[0].(map[string]any)["symbol_id"])
}

func TestExtractCandidates_LimitTruncates(t *testing.T) {
	s := newExtractTestServer(t)
	out := callExtractHandler(t, s, map[string]any{
		"min_lines":   1,
		"min_callers": 0,
		"min_fan_out": 0,
		"limit":       1,
	})
	cands, _ := out["candidates"].([]any)
	assert.Len(t, cands, 1)
	assert.Equal(t, true, out["truncated"])
}

func TestExtractCandidates_ThresholdsEchoed(t *testing.T) {
	s := newExtractTestServer(t)
	out := callExtractHandler(t, s, map[string]any{
		"min_lines":   30,
		"min_callers": 3,
		"min_fan_out": 7,
	})
	th := out["thresholds"].(map[string]any)
	assert.EqualValues(t, 30, th["min_lines"].(float64))
	assert.EqualValues(t, 3, th["min_callers"].(float64))
	assert.EqualValues(t, 7, th["min_fan_out"].(float64))
}

func TestExtractCandidates_RationaleSignalNaming(t *testing.T) {
	cases := map[string]struct {
		line, caller, fanOut int
		mustContain          []string
	}{
		"very-long":     {60, 2, 5, []string{"very long"}},
		"widely-called": {30, 15, 5, []string{"widely called"}},
		"orchestration": {30, 5, 20, []string{"orchestration shape"}},
		"baseline":      {21, 2, 5, []string{"long", "multi-caller", "complex body"}},
	}
	for name, c := range cases {
		got := buildExtractRationale(c.line, c.caller, c.fanOut)
		for _, sub := range c.mustContain {
			assert.Contains(t, got, sub, "%s: rationale=%q", name, got)
		}
	}
}
