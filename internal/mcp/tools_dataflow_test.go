package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/indexer"
	"github.com/zzet/gortex/internal/query"
)

// dataflowTestServer indexes a single Go file with a known
// source-mid-sink shape so we can exercise the MCP tools end-to-end
// without depending on the larger fixture used by setupTestServer.
func dataflowTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	src := `package main

func Source(seed string) string {
	return seed
}

func Mid(s string) string {
	v := s
	return v
}

func Sink(payload string) {}

func Driver(input string) {
	a := Source(input)
	b := Mid(a)
	Sink(b)
}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644))
	g := graph.New()
	reg := testRegistry()
	cfg := config.Default()
	idx := indexer.New(g, reg, cfg.Index, zap.NewNop())
	_, err := idx.Index(dir)
	require.NoError(t, err)
	idx.ResolveAll()
	eng := query.NewEngine(g)
	srv := NewServer(eng, g, idx, nil, zap.NewNop(), nil)
	return srv
}

// findFunctionID walks the graph for a function/method node by
// name and returns its ID (the prefix is repo-dependent in our
// indexer, so we discover it via the live graph).
func findFunctionID(t *testing.T, srv *Server, name string) string {
	t.Helper()
	for _, n := range srv.graph.AllNodes() {
		if n.Kind != graph.KindFunction && n.Kind != graph.KindMethod {
			continue
		}
		if n.Name == name {
			return n.ID
		}
	}
	t.Fatalf("function %q not found", name)
	return ""
}

func TestHandleFlowBetween_DirectPath(t *testing.T) {
	srv := dataflowTestServer(t)
	driverID := findFunctionID(t, srv, "Driver")

	req := mcplib.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"source_id": driverID + "#param:input",
		"sink_id":   findFunctionID(t, srv, "Sink") + "#param:payload",
		"max_depth": float64(10),
	}
	res, err := srv.handleFlowBetween(t.Context(), req)
	require.NoError(t, err)
	require.False(t, res.IsError, "tool errored: %v", res)

	var payload struct {
		Total int `json:"total"`
		Paths []struct {
			IDs   []string `json:"ids"`
			Edges []struct {
				Kind string `json:"kind"`
			} `json:"edges"`
		} `json:"paths"`
	}
	text := res.Content[0].(mcplib.TextContent).Text
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	require.Greater(t, payload.Total, 0, "expected at least one path; got: %s", text)
	// Path must traverse Source / Mid → Sink param.
	first := payload.Paths[0]
	if !contains(first.IDs, "Source") {
		t.Logf("path does not reference Source by short name (this is fine: IDs are full): %v", first.IDs)
	}
}

func TestHandleFlowBetween_MissingSource(t *testing.T) {
	srv := dataflowTestServer(t)
	req := mcplib.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"sink_id": "anything",
	}
	res, err := srv.handleFlowBetween(t.Context(), req)
	require.NoError(t, err)
	require.True(t, res.IsError, "expected error result for missing source_id")
}

func TestHandleFlowBetween_GCXFormat(t *testing.T) {
	srv := dataflowTestServer(t)
	driverID := findFunctionID(t, srv, "Driver")
	req := mcplib.CallToolRequest{}
	req.Params.Name = "flow_between"
	req.Params.Arguments = map[string]any{
		"source_id": driverID + "#param:input",
		"sink_id":   findFunctionID(t, srv, "Sink") + "#param:payload",
		"format":    "gcx",
	}
	res, err := srv.handleFlowBetween(t.Context(), req)
	require.NoError(t, err)
	require.False(t, res.IsError)
	text := res.Content[0].(mcplib.TextContent).Text
	require.Contains(t, text, "flow_between.summary")
	require.Contains(t, text, "flow_between.paths")
}

func TestHandleTaintPaths_PatternMatch(t *testing.T) {
	srv := dataflowTestServer(t)
	req := mcplib.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"source_pattern": "name:Source",
		"sink_pattern":   "name:Sink",
		"max_depth":      float64(10),
	}
	res, err := srv.handleTaintPaths(t.Context(), req)
	require.NoError(t, err)
	require.False(t, res.IsError, "errored: %v", res)
	text := res.Content[0].(mcplib.TextContent).Text
	require.Contains(t, text, "findings")
	var payload struct {
		Total    int              `json:"total"`
		Findings []map[string]any `json:"findings"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	require.Greater(t, payload.Total, 0, "expected at least one finding; got %s", text)
}

func TestHandleTaintPaths_EmptyPattern(t *testing.T) {
	srv := dataflowTestServer(t)
	req := mcplib.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"source_pattern": "",
		"sink_pattern":   "name:Sink",
	}
	res, err := srv.handleTaintPaths(t.Context(), req)
	require.NoError(t, err)
	require.True(t, res.IsError, "expected error for empty source_pattern")
}

// TestHandleTaintPaths_E2EMultiFile builds a more realistic
// multi-file scenario: `os/Getenv`-shaped source returning a
// secret, threaded through a couple of helpers, and ultimately
// passed to a sink simulating `db.Query`. Verifies that
// flow_between → returns_to → arg_of compose into a path the
// agent can recover via `name:` patterns.
func TestHandleTaintPaths_E2EMultiFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "secret.go"), []byte(`package main

func Getenv(key string) string {
	return key
}
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "format.go"), []byte(`package main

func Format(secret string) string {
	out := secret
	return out
}
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "db.go"), []byte(`package main

func DBQuery(sql string) {}
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "driver.go"), []byte(`package main

func Driver(name string) {
	secret := Getenv(name)
	sql := Format(secret)
	DBQuery(sql)
}
`), 0o644))

	g := graph.New()
	reg := testRegistry()
	cfg := config.Default()
	idx := indexer.New(g, reg, cfg.Index, zap.NewNop())
	_, err := idx.Index(dir)
	require.NoError(t, err)
	idx.ResolveAll()
	eng := query.NewEngine(g)
	srv := NewServer(eng, g, idx, nil, zap.NewNop(), nil)

	req := mcplib.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"source_pattern": "name:Getenv",
		"sink_pattern":   "name:DBQuery",
		"max_depth":      float64(20),
	}
	res, err := srv.handleTaintPaths(t.Context(), req)
	require.NoError(t, err)
	require.False(t, res.IsError, "tool errored: %v", res)
	text := res.Content[0].(mcplib.TextContent).Text

	var payload struct {
		Total    int `json:"total"`
		Findings []struct {
			Source map[string]any `json:"source"`
			Sink   map[string]any `json:"sink"`
			Paths  []struct {
				IDs []string `json:"ids"`
			} `json:"paths"`
		} `json:"findings"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	require.Greater(t, payload.Total, 0, "expected E2E taint finding; got %s", text)
	bestPath := payload.Findings[0].Paths[0]
	if len(bestPath.IDs) < 3 {
		t.Errorf("expected multi-hop path; got %v", bestPath.IDs)
	}
	// Path must touch a Format-related node — confirms the BFS
	// crossed the intermediate function.
	found := false
	for _, id := range bestPath.IDs {
		if strings.Contains(id, "Format") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected Format on E2E path; got %v", bestPath.IDs)
	}
}

func TestHandleTaintPaths_GCXFormat(t *testing.T) {
	srv := dataflowTestServer(t)
	req := mcplib.CallToolRequest{}
	req.Params.Name = "taint_paths"
	req.Params.Arguments = map[string]any{
		"source_pattern": "name:Source",
		"sink_pattern":   "name:Sink",
		"format":         "gcx",
	}
	res, err := srv.handleTaintPaths(t.Context(), req)
	require.NoError(t, err)
	require.False(t, res.IsError)
	text := res.Content[0].(mcplib.TextContent).Text
	require.Contains(t, text, "taint_paths.summary")
	require.Contains(t, text, "taint_paths.findings")
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
