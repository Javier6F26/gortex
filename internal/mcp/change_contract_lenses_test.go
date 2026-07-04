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

func TestIsExportedChange(t *testing.T) {
	require.True(t, isExportedChange(&graph.Node{Name: "Foo"}))
	require.False(t, isExportedChange(&graph.Node{Name: "foo"}))
	require.True(t, isExportedChange(&graph.Node{Name: "foo", Meta: map[string]any{"visibility": "public"}}))
	require.False(t, isExportedChange(&graph.Node{Name: "Foo", Meta: map[string]any{"visibility": "private"}}))
}

func TestCoChangeOmissions(t *testing.T) {
	s := &Server{}
	scores := map[string]map[string]float64{
		"a.go": {"b.go": 0.8, "c.go": 0.3, "d.go": 0.6},
	}
	s.storeCoChange(scores, map[string]map[string]int{})

	// Touching a.go: b.go (0.8) and d.go (0.6) are above threshold and omitted;
	// c.go (0.3) is below threshold.
	reasons := s.coChangeOmissions([]string{"a.go"})
	require.Len(t, reasons, 2)
	files := map[string]bool{}
	for _, r := range reasons {
		require.Equal(t, "co_change_omission", r.Family)
		require.Equal(t, "warn", r.Severity)
		files[r.Symbol] = true
	}
	require.True(t, files["b.go"])
	require.True(t, files["d.go"])
	require.False(t, files["c.go"])

	// When a partner is already in the touched set it is not an omission.
	reasons = s.coChangeOmissions([]string{"a.go", "b.go", "d.go"})
	require.Empty(t, reasons)
}

func setupAPIServer(t *testing.T) (*Server, graph.Store) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "api.go"), []byte(`package svc

func Foo(x int) int { return x * 2 }
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "consumer.go"), []byte(`package svc

func Bar() int { return Foo(21) }
`), 0o644))

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

func TestAPIDriftLens(t *testing.T) {
	srv, g := setupAPIServer(t)

	var fooID string
	for _, n := range g.AllNodes() {
		if n.Name == "Foo" && n.Kind == graph.KindFunction {
			fooID = n.ID
		}
	}
	require.NotEmpty(t, fooID, "Foo not indexed")

	req := mcplib.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"source":  "symbols",
		"symbols": fooID,
		"lens":    "api",
	}
	res, err := srv.handleChangeContract(context.Background(), req)
	require.NoError(t, err)
	require.False(t, res.IsError, "change_contract errored: %s", toolResultText(res))

	var env changeEnvelope
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].(mcplib.TextContent).Text), &env))

	require.NotEmpty(t, env.APISurface, "exported Foo should appear on the API surface")
	require.Equal(t, "Foo", env.APISurface[0].Name)
	require.GreaterOrEqual(t, env.APISurface[0].ExternalCallers, 1, "Bar in consumer.go is an external caller")

	// An exported symbol with external callers drives an api_drift warning.
	foundDrift := false
	for _, r := range env.Reasons {
		if r.Family == "api_drift" {
			foundDrift = true
		}
	}
	require.True(t, foundDrift, "expected an api_drift reason, got %+v", env.Reasons)
	require.Equal(t, verdictWarn, env.Verdict)
}
