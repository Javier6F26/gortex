package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/indexer"
	"github.com/zzet/gortex/internal/query"
)

// cfgTestServer indexes one Go file with a branchy function so the
// CFG tools can be exercised end-to-end against a real graph +
// on-disk source.
func cfgTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	src := `package main

func Classify(score int) string {
	label := "low"
	if score > 90 {
		label = "high"
	} else if score > 50 {
		label = "mid"
	}
	for i := 0; i < score; i++ {
		if i == 3 {
			break
		}
	}
	return label
}

var topLevel = 1
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644))
	g := graph.New()
	reg := testRegistry()
	cfgConf := config.Default()
	idx := indexer.New(g, reg, cfgConf.Index, zap.NewNop())
	_, err := idx.Index(dir)
	require.NoError(t, err)
	idx.ResolveAll()
	eng := query.NewEngine(g)
	return NewServer(eng, g, idx, nil, zap.NewNop(), nil)
}

func cfgFindSymbol(t *testing.T, srv *Server, name string, kinds ...graph.NodeKind) string {
	t.Helper()
	kindOK := func(k graph.NodeKind) bool {
		if len(kinds) == 0 {
			return true
		}
		for _, want := range kinds {
			if k == want {
				return true
			}
		}
		return false
	}
	for _, n := range srv.graph.AllNodes() {
		if n.Name == name && kindOK(n.Kind) {
			return n.ID
		}
	}
	t.Fatalf("symbol %q not found", name)
	return ""
}

func TestHandleGetCFG_BlocksEdgesChains(t *testing.T) {
	srv := cfgTestServer(t)
	id := cfgFindSymbol(t, srv, "Classify", graph.KindFunction)

	req := mcplib.CallToolRequest{}
	req.Params.Arguments = map[string]any{"id": id}
	res, err := srv.handleGetCFG(t.Context(), req)
	require.NoError(t, err)
	require.False(t, res.IsError, "tool errored: %v", res)

	var payload struct {
		Name   string `json:"name"`
		Blocks []struct {
			ID         int `json:"id"`
			Statements []struct {
				Text string   `json:"text"`
				Defs []string `json:"defs"`
				Uses []string `json:"uses"`
			} `json:"statements"`
		} `json:"blocks"`
		Edges []struct {
			From  int    `json:"from"`
			To    int    `json:"to"`
			Label string `json:"label"`
		} `json:"edges"`
		DefUse []struct {
			Stmt int    `json:"stmt"`
			Var  string `json:"var"`
			Defs []int  `json:"defs"`
		} `json:"def_use"`
		TotalBlocks int `json:"total_blocks"`
	}
	text := res.Content[0].(mcplib.TextContent).Text
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	require.Equal(t, "Classify", payload.Name)
	require.Greater(t, payload.TotalBlocks, 4, "branchy function needs several blocks")

	labels := map[string]bool{}
	for _, e := range payload.Edges {
		labels[e.Label] = true
	}
	for _, want := range []string{"true", "false", "loop_back", "break", "return"} {
		require.True(t, labels[want], "missing %s edge in %s", want, text)
	}

	// The return's use of label must chain to all three defs.
	var labelChain []int
	for _, ch := range payload.DefUse {
		if ch.Var == "label" {
			labelChain = ch.Defs
		}
	}
	require.Len(t, labelChain, 3, "label has three reaching defs at the return: %s", text)
}

func TestHandleGetCFG_Mermaid(t *testing.T) {
	srv := cfgTestServer(t)
	id := cfgFindSymbol(t, srv, "Classify", graph.KindFunction)

	req := mcplib.CallToolRequest{}
	req.Params.Arguments = map[string]any{"id": id, "mermaid": true}
	res, err := srv.handleGetCFG(t.Context(), req)
	require.NoError(t, err)
	require.False(t, res.IsError)
	text := res.Content[0].(mcplib.TextContent).Text
	var payload struct {
		Mermaid string `json:"mermaid"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	require.Contains(t, payload.Mermaid, "flowchart TD")
	require.Contains(t, payload.Mermaid, "-->|true|")
}

func TestHandleGetCFG_GCXFormat(t *testing.T) {
	srv := cfgTestServer(t)
	id := cfgFindSymbol(t, srv, "Classify", graph.KindFunction)

	req := mcplib.CallToolRequest{}
	req.Params.Name = "get_cfg"
	req.Params.Arguments = map[string]any{"id": id, "format": "gcx", "mermaid": true}
	res, err := srv.handleGetCFG(t.Context(), req)
	require.NoError(t, err)
	require.False(t, res.IsError)
	text := res.Content[0].(mcplib.TextContent).Text
	for _, section := range []string{"get_cfg.summary", "get_cfg.stmts", "get_cfg.edges", "get_cfg.chains", "get_cfg.mermaid"} {
		require.Contains(t, text, section)
	}
}

func TestHandleGetCFG_Errors(t *testing.T) {
	srv := cfgTestServer(t)

	// Missing id.
	req := mcplib.CallToolRequest{}
	req.Params.Arguments = map[string]any{}
	res, err := srv.handleGetCFG(t.Context(), req)
	require.NoError(t, err)
	require.True(t, res.IsError)

	// Unknown symbol.
	req.Params.Arguments = map[string]any{"id": "nope.go::Missing"}
	res, err = srv.handleGetCFG(t.Context(), req)
	require.NoError(t, err)
	require.True(t, res.IsError)

	// Non-function symbol.
	varID := cfgFindSymbol(t, srv, "topLevel")
	req.Params.Arguments = map[string]any{"id": varID}
	res, err = srv.handleGetCFG(t.Context(), req)
	require.NoError(t, err)
	require.True(t, res.IsError, "get_cfg on a variable must error")
}

func TestHandleAnalyzeDefUse_ThroughDispatcher(t *testing.T) {
	srv := cfgTestServer(t)
	id := cfgFindSymbol(t, srv, "Classify", graph.KindFunction)

	req := mcplib.CallToolRequest{}
	req.Params.Arguments = map[string]any{"kind": "def_use", "ids": id}
	res, err := srv.handleAnalyze(t.Context(), req)
	require.NoError(t, err)
	require.False(t, res.IsError, "analyze def_use errored: %v", res)

	var payload struct {
		Total   int `json:"total"`
		Symbols []struct {
			ID     string `json:"id"`
			Error  string `json:"error"`
			Chains []struct {
				Var      string `json:"var"`
				StmtLine int    `json:"stmt_line"`
				DefLines []int  `json:"def_lines"`
			} `json:"chains"`
			Variables []struct {
				Var  string `json:"var"`
				Defs int    `json:"defs"`
				Uses int    `json:"uses"`
			} `json:"variables"`
		} `json:"symbols"`
	}
	text := res.Content[0].(mcplib.TextContent).Text
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	require.Equal(t, 1, payload.Total)
	sym := payload.Symbols[0]
	require.Empty(t, sym.Error)
	require.NotEmpty(t, sym.Chains)

	// label's per-variable summary: 3 defs (init + two arms).
	foundLabel := false
	for _, v := range sym.Variables {
		if v.Var == "label" {
			foundLabel = true
			require.Equal(t, 3, v.Defs, "label is defined three times")
			require.GreaterOrEqual(t, v.Uses, 1)
		}
	}
	require.True(t, foundLabel, "variables rollup must include label: %s", text)
}

func TestHandleAnalyzeDefUse_DegradesPerSymbol(t *testing.T) {
	srv := cfgTestServer(t)
	good := cfgFindSymbol(t, srv, "Classify", graph.KindFunction)

	req := mcplib.CallToolRequest{}
	req.Params.Arguments = map[string]any{"kind": "def_use", "ids": good + ",missing.go::Nope"}
	res, err := srv.handleAnalyze(t.Context(), req)
	require.NoError(t, err)
	require.False(t, res.IsError)

	var payload struct {
		Symbols []struct {
			ID    string `json:"id"`
			Error string `json:"error"`
		} `json:"symbols"`
	}
	text := res.Content[0].(mcplib.TextContent).Text
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	require.Len(t, payload.Symbols, 2)
	require.Empty(t, payload.Symbols[0].Error)
	require.NotEmpty(t, payload.Symbols[1].Error, "missing symbol must degrade to a per-symbol error")
}

func TestHandleAnalyzeDefUse_GCX(t *testing.T) {
	srv := cfgTestServer(t)
	id := cfgFindSymbol(t, srv, "Classify", graph.KindFunction)

	req := mcplib.CallToolRequest{}
	req.Params.Name = "analyze"
	req.Params.Arguments = map[string]any{"kind": "def_use", "ids": id, "format": "gcx"}
	res, err := srv.handleAnalyze(t.Context(), req)
	require.NoError(t, err)
	require.False(t, res.IsError)
	text := res.Content[0].(mcplib.TextContent).Text
	require.Contains(t, text, "analyze.def_use")
}

func TestHandleAnalyzeDefUse_RequiresIDs(t *testing.T) {
	srv := cfgTestServer(t)
	req := mcplib.CallToolRequest{}
	req.Params.Arguments = map[string]any{"kind": "def_use"}
	res, err := srv.handleAnalyze(t.Context(), req)
	require.NoError(t, err)
	require.True(t, res.IsError)
}

// TestHandleFlowBetween_RefinementLive proves the reaching-
// definitions refinement runs on the real flow_between path: the
// indexed fixture's Mid function binds `v := s`, so the
// param-s → local-v hop must come back stamped
// confirmed_intraprocedural.
func TestHandleFlowBetween_RefinementLive(t *testing.T) {
	srv := dataflowTestServer(t)
	driverID := findFunctionID(t, srv, "Driver")
	sinkID := findFunctionID(t, srv, "Sink")

	req := mcplib.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"source_id": driverID + "#param:input",
		"sink_id":   sinkID + "#param:payload",
		"max_depth": float64(10),
	}
	res, err := srv.handleFlowBetween(t.Context(), req)
	require.NoError(t, err)
	require.False(t, res.IsError, "tool errored: %v", res)

	var payload struct {
		Paths []struct {
			Edges []struct {
				From    string `json:"from"`
				To      string `json:"to"`
				Kind    string `json:"kind"`
				Refined string `json:"refined"`
			} `json:"edges"`
		} `json:"paths"`
	}
	text := res.Content[0].(mcplib.TextContent).Text
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	require.NotEmpty(t, payload.Paths)

	confirmed := 0
	for _, p := range payload.Paths {
		for _, e := range p.Edges {
			require.NotEqual(t, "pruned", e.Refined, "fixture has no stale flows: %s -> %s", e.From, e.To)
			if e.Refined == "confirmed_intraprocedural" {
				confirmed++
			}
		}
	}
	require.Greater(t, confirmed, 0, "at least one same-function value_flow hop must be confirmed: %s", text)
}
