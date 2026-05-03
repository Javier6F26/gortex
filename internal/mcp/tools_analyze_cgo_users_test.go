package mcp

import (
	"context"
	"encoding/json"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/zzet/gortex/internal/graph"
)

func callAnalyzeCgoUsers(t *testing.T, srv *Server, args map[string]any) map[string]any {
	t.Helper()
	args["kind"] = "cgo_users"
	req := mcplib.CallToolRequest{}
	req.Params.Name = "analyze"
	req.Params.Arguments = args
	res, err := srv.handleAnalyze(context.Background(), req)
	if err != nil {
		t.Fatalf("handleAnalyze: %v", err)
	}
	if res.IsError {
		t.Fatalf("error: %+v", res.Content)
	}
	textBlock := res.Content[0].(mcplib.TextContent)
	var out map[string]any
	if err := json.Unmarshal([]byte(textBlock.Text), &out); err != nil {
		t.Fatalf("json: %v\n%s", err, textBlock.Text)
	}
	return out
}

func TestAnalyzeCgoUsers_ListsFlaggedFiles(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.graph.AddNode(&graph.Node{
		ID:       "pkg/c.go",
		Kind:     graph.KindFile,
		FilePath: "pkg/c.go",
		Meta:     map[string]any{"uses_cgo": true},
	})
	srv.graph.AddNode(&graph.Node{
		ID:       "pkg/regular.go",
		Kind:     graph.KindFile,
		FilePath: "pkg/regular.go",
	})
	srv.graph.AddNode(&graph.Node{
		ID:       "pkg/another_c.go",
		Kind:     graph.KindFile,
		FilePath: "pkg/another_c.go",
		Meta:     map[string]any{"uses_cgo": true},
	})

	out := callAnalyzeCgoUsers(t, srv, map[string]any{})
	rows, _ := out["cgo_users"].([]any)
	if len(rows) != 2 {
		t.Fatalf("expected 2 cgo files, got %d", len(rows))
	}
	// Path-sorted output: another_c.go first.
	first, _ := rows[0].(map[string]any)
	if first["file"] != "pkg/another_c.go" {
		t.Errorf("expected another_c.go first (path sort), got %v", first["file"])
	}
}

func TestAnalyzeCgoUsers_NoCgoFilesYieldsEmpty(t *testing.T) {
	srv, _ := setupTestServer(t)
	out := callAnalyzeCgoUsers(t, srv, map[string]any{})
	rows, _ := out["cgo_users"].([]any)
	if len(rows) != 0 {
		t.Errorf("expected zero rows, got %d", len(rows))
	}
}
