package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func decodeFileOpsResult(t *testing.T, result *mcplib.CallToolResult) map[string]any {
	t.Helper()
	require.NotEmpty(t, result.Content)
	text := result.Content[0].(mcplib.TextContent).Text
	var resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &resp))
	return resp
}

func TestWriteFile_CreatesNewFile(t *testing.T) {
	srv, dir := setupTestServer(t)

	result := callTool(t, srv, "write_file", map[string]any{
		"path":    "docs/intro.md",
		"content": "# Intro\n\nHello world.\n",
	})
	assert.False(t, result.IsError)
	resp := decodeFileOpsResult(t, result)
	assert.Equal(t, "created", resp["status"])

	got, err := os.ReadFile(filepath.Join(dir, "docs", "intro.md"))
	require.NoError(t, err)
	assert.Equal(t, "# Intro\n\nHello world.\n", string(got))
	assert.Equal(t, float64(len(got)), resp["bytes_written"])
}

func TestWriteFile_OverwritesExistingFile(t *testing.T) {
	srv, dir := setupTestServer(t)
	target := filepath.Join(dir, "notes.txt")
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o644))

	result := callTool(t, srv, "write_file", map[string]any{
		"path":    "notes.txt",
		"content": "new content",
	})
	assert.False(t, result.IsError)
	resp := decodeFileOpsResult(t, result)
	assert.Equal(t, "overwritten", resp["status"])

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "new content", string(got))
}

func TestWriteFile_AbsolutePath(t *testing.T) {
	srv, dir := setupTestServer(t)
	target := filepath.Join(dir, "absolute.txt")

	result := callTool(t, srv, "write_file", map[string]any{
		"path":    target,
		"content": "abs",
	})
	assert.False(t, result.IsError)

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "abs", string(got))
}

func TestWriteFile_ReindexesGoSource(t *testing.T) {
	srv, _ := setupTestServer(t)

	result := callTool(t, srv, "write_file", map[string]any{
		"path":    "main.go",
		"content": "package main\n\nfunc freshlyAdded() {}\n",
	})
	assert.False(t, result.IsError)
	resp := decodeFileOpsResult(t, result)
	assert.Equal(t, true, resp["reindexed"], "Go file inside repo must trigger reindex")

	search := callTool(t, srv, "search_symbols", map[string]any{"query": "freshlyAdded"})
	searchResp := decodeFileOpsResult(t, search)
	assert.Greater(t, searchResp["total"], float64(0), "new symbol should be searchable post-reindex")
}

func TestEditFile_UniqueReplacement(t *testing.T) {
	srv, dir := setupTestServer(t)
	target := filepath.Join(dir, "changelog.md")
	require.NoError(t, os.WriteFile(target, []byte("v0.1 released\nv0.2 pending\n"), 0o644))

	result := callTool(t, srv, "edit_file", map[string]any{
		"path":       "changelog.md",
		"old_string": "v0.2 pending",
		"new_string": "v0.2 shipped",
	})
	assert.False(t, result.IsError)
	resp := decodeFileOpsResult(t, result)
	assert.Equal(t, "applied", resp["status"])
	assert.Equal(t, float64(1), resp["replacements"])

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "v0.1 released\nv0.2 shipped\n", string(got))
}

func TestEditFile_AmbiguousMatchRejected(t *testing.T) {
	srv, dir := setupTestServer(t)
	target := filepath.Join(dir, "dup.md")
	require.NoError(t, os.WriteFile(target, []byte("TODO\nTODO\n"), 0o644))

	result := callTool(t, srv, "edit_file", map[string]any{
		"path":       "dup.md",
		"old_string": "TODO",
		"new_string": "DONE",
	})
	assert.True(t, result.IsError, "multiple matches without replace_all must error")

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "TODO\nTODO\n", string(got), "file must be untouched on error")
}

func TestEditFile_ReplaceAll(t *testing.T) {
	srv, dir := setupTestServer(t)
	target := filepath.Join(dir, "dup.md")
	require.NoError(t, os.WriteFile(target, []byte("TODO\nTODO\nTODO\n"), 0o644))

	result := callTool(t, srv, "edit_file", map[string]any{
		"path":        "dup.md",
		"old_string":  "TODO",
		"new_string":  "DONE",
		"replace_all": true,
	})
	assert.False(t, result.IsError)
	resp := decodeFileOpsResult(t, result)
	assert.Equal(t, float64(3), resp["replacements"])

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "DONE\nDONE\nDONE\n", string(got))
}

func TestEditFile_MissingOldString(t *testing.T) {
	srv, dir := setupTestServer(t)
	target := filepath.Join(dir, "file.md")
	require.NoError(t, os.WriteFile(target, []byte("content"), 0o644))

	result := callTool(t, srv, "edit_file", map[string]any{
		"path":       "file.md",
		"old_string": "not present",
		"new_string": "x",
	})
	assert.True(t, result.IsError)
}

func TestEditFile_IdenticalStringsRejected(t *testing.T) {
	srv, dir := setupTestServer(t)
	target := filepath.Join(dir, "file.md")
	require.NoError(t, os.WriteFile(target, []byte("content"), 0o644))

	result := callTool(t, srv, "edit_file", map[string]any{
		"path":       "file.md",
		"old_string": "content",
		"new_string": "content",
	})
	assert.True(t, result.IsError)
}

func TestEditFile_NonexistentFile(t *testing.T) {
	srv, _ := setupTestServer(t)

	result := callTool(t, srv, "edit_file", map[string]any{
		"path":       "does-not-exist.md",
		"old_string": "a",
		"new_string": "b",
	})
	assert.True(t, result.IsError)
}

func TestWriteFile_PreservesExistingMode(t *testing.T) {
	srv, dir := setupTestServer(t)
	target := filepath.Join(dir, "exec.sh")
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o755))

	result := callTool(t, srv, "write_file", map[string]any{
		"path":    "exec.sh",
		"content": "new",
	})
	assert.False(t, result.IsError)

	info, err := os.Stat(target)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())
}
