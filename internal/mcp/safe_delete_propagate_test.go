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

func TestClassifyCallSite(t *testing.T) {
	a, _ := classifyCallSite("\tTarget()", "Target")
	require.Equal(t, "remove_line", a)

	a, _ = classifyCallSite("\tobj.Target()", "Target")
	require.Equal(t, "remove_line", a)

	a, _ = classifyCallSite("\tx := Target()", "Target")
	require.Equal(t, "manual", a)

	a, _ = classifyCallSite("\treturn Target()", "Target")
	require.Equal(t, "manual", a)

	a, _ = classifyCallSite("\tn := a + Target()", "Target")
	require.Equal(t, "manual", a)

	a, _ = classifyCallSite("\tdefer Target()", "Target")
	require.Equal(t, "remove_line", a)
}

func setupDeleteServer(t *testing.T) (*Server, graph.Store, string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "del.go"), []byte(`package p

func Target() {}
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "caller.go"), []byte(`package p

func Caller() {
	Target()
}
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
	return srv, g, dir
}

func TestPropagateDeletePlanAndApply(t *testing.T) {
	srv, g, dir := setupDeleteServer(t)

	var targetID string
	for _, n := range g.AllNodes() {
		if n.Name == "Target" && n.Kind == graph.KindFunction {
			targetID = n.ID
		}
	}
	require.NotEmpty(t, targetID)

	callRes := func(args map[string]any) map[string]any {
		req := mcplib.CallToolRequest{}
		req.Params.Arguments = args
		res, err := srv.handleSafeDeleteSymbol(context.Background(), req)
		require.NoError(t, err)
		require.False(t, res.IsError, "safe_delete errored: %s", toolResultText(res))
		var payload map[string]any
		require.NoError(t, json.Unmarshal([]byte(res.Content[0].(mcplib.TextContent).Text), &payload))
		return payload
	}

	// 1. Plan (dry-run): one removable caller patch.
	plan := callRes(map[string]any{"id": targetID, "propagate": true, "dry_run": true})
	require.Equal(t, "propagation_plan", plan["status"])
	require.Equal(t, float64(1), plan["removable"])
	require.Equal(t, float64(0), plan["manual"])

	// 2. Apply: patches the caller and deletes the target.
	applied := callRes(map[string]any{"id": targetID, "propagate": true, "dry_run": false})
	status, _ := applied["status"].(string)
	require.Contains(t, []string{"deleted", "deleted_by_propagation"}, status)

	// The caller's standalone Target() call was removed.
	callerSrc, err := os.ReadFile(filepath.Join(dir, "caller.go"))
	require.NoError(t, err)
	require.NotContains(t, string(callerSrc), "Target()", "the call site should be removed")
}
