package mcp

import (
	"context"
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

func TestCCImpactTier(t *testing.T) {
	require.Equal(t, "High", ccImpactTier(200, 0))
	require.Equal(t, "High", ccImpactTier(10, 20))
	require.Equal(t, "Medium", ccImpactTier(60, 0))
	require.Equal(t, "Medium", ccImpactTier(0, 10))
	require.Equal(t, "Low", ccImpactTier(10, 2))
}

func TestDominantSymbol(t *testing.T) {
	s := &Server{}
	nodes := []*graph.Node{
		{ID: "a", Kind: graph.KindFunction, StartLine: 1, EndLine: 5},
		{ID: "b", Kind: graph.KindMethod, StartLine: 10, EndLine: 90},
		{ID: "c", Kind: graph.KindType, StartLine: 1, EndLine: 200}, // not a func/method
	}
	got := s.dominantSymbol(nodes)
	require.NotNil(t, got)
	require.Equal(t, "b", got.ID)

	require.Nil(t, s.dominantSymbol([]*graph.Node{{ID: "t", Kind: graph.KindType}}))
}

func setupStrategyServer(t *testing.T) (*Server, graph.Store) {
	t.Helper()
	dir := t.TempDir()
	src := `package big

func Wide(a, b, c, d, e, f int) int {
	return a + b + c + d + e + f
}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "big.go"), []byte(src), 0o644))

	g := graph.New()
	reg := testRegistry()
	cfg := config.Default()
	idx := indexer.New(g, reg, cfg.Index, zap.NewNop())
	_, err := idx.Index(dir)
	require.NoError(t, err)

	eng := query.NewEngine(g)
	srv := NewServer(eng, g, idx, nil, zap.NewNop(), nil)
	srv.RunAnalysis()
	return srv, g
}

func TestEditStrategyIntroduceParameterObject(t *testing.T) {
	srv, g := setupStrategyServer(t)

	var wideID string
	for _, n := range g.AllNodes() {
		if n.Name == "Wide" && n.Kind == graph.KindFunction {
			wideID = n.ID
		}
	}
	require.NotEmpty(t, wideID, "Wide function not indexed")

	req := mcplib.CallToolRequest{}
	req.Params.Arguments = map[string]any{"source": "symbols", "symbols": wideID}
	res, err := srv.handleChangeContract(context.Background(), req)
	require.NoError(t, err)
	require.False(t, res.IsError, "change_contract errored: %s", toolResultText(res))

	var env changeEnvelope
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].(mcplib.TextContent).Text), &env))
	require.NotNil(t, env.EditStrategy, "a 6-parameter function should get an edit_strategy")
	require.Equal(t, "Introduce Parameter Object", env.EditStrategy.Technique)
	require.NotEmpty(t, env.EditStrategy.Steps)
	require.NotEmpty(t, env.EditStrategy.Safety)
}
