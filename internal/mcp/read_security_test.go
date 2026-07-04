package mcp

import (
	"context"
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

func newReadGuardServer(t *testing.T, repoRoot string) *Server {
	t.Helper()
	g := graph.New()
	reg := testRegistry()
	idx := indexer.New(g, reg, config.IndexConfig{}, zap.NewNop())
	idx.SetRootPath(repoRoot)
	return NewServer(query.NewEngine(g), g, idx, nil, zap.NewNop(), nil)
}

func readFileTool(t *testing.T, srv *Server, args map[string]any) *mcplib.CallToolResult {
	t.Helper()
	req := mcplib.CallToolRequest{}
	req.Params.Name = "read_file"
	req.Params.Arguments = args
	res, err := srv.handleReadFile(context.Background(), req)
	require.NoError(t, err)
	return res
}

func readText(t *testing.T, res *mcplib.CallToolResult) string {
	t.Helper()
	text, _ := singleTextContent(res)
	return text
}

// TestSymlinkGuardRefusesEscape proves the path-traversal guard: read_file
// refuses any path whose REAL location escapes the indexed repository root —
// an in-repo symlink pointing out, or a bare absolute path — while still
// serving in-repo files and in-repo symlinks.
func TestSymlinkGuardRefusesEscape(t *testing.T) {
	repoRoot := t.TempDir()
	outside := t.TempDir()

	secret := filepath.Join(outside, "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("TOP-SECRET-VALUE"), 0o600))
	inside := filepath.Join(repoRoot, "ok.go")
	require.NoError(t, os.WriteFile(inside, []byte("package x\n"), 0o644))

	escape := filepath.Join(repoRoot, "escape.txt")
	require.NoError(t, os.Symlink(secret, escape))
	internalLink := filepath.Join(repoRoot, "in-link.go")
	require.NoError(t, os.Symlink(inside, internalLink))

	srv := newReadGuardServer(t, repoRoot)

	// 1. An in-repo symlink escaping the repo is refused; the secret never leaks.
	esc := readFileTool(t, srv, map[string]any{"path": escape})
	require.True(t, esc.IsError, "a symlink escaping the repo must be refused")
	require.NotContains(t, readText(t, esc), "TOP-SECRET-VALUE")
	require.Contains(t, readText(t, esc), "outside every indexed repository")

	// 2. A bare absolute path outside the repo is refused too.
	abs := readFileTool(t, srv, map[string]any{"path": secret})
	require.True(t, abs.IsError, "an absolute path outside the repo must be refused")
	require.NotContains(t, readText(t, abs), "TOP-SECRET-VALUE")

	// 3. A normal in-repo file reads fine (no false positive).
	ok := readFileTool(t, srv, map[string]any{"path": inside})
	require.False(t, ok.IsError, "an in-repo file must read: %s", readText(t, ok))

	// 4. An in-repo symlink to another in-repo file reads fine.
	intl := readFileTool(t, srv, map[string]any{"path": internalLink})
	require.False(t, intl.IsError, "an in-repo symlink must read: %s", readText(t, intl))
}

// TestConfigRedactAcrossReadTools proves the key-only config redaction is
// applied on the read_file surface: a secret-shaped value is withheld by
// default and served verbatim only with allow_secrets.
func TestConfigRedactAcrossReadTools(t *testing.T) {
	repoRoot := t.TempDir()
	const secretVal = "ghp_0123456789abcdefghijklmnopqrstuvwxyz"
	cfgPath := filepath.Join(repoRoot, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("token: "+secretVal+"\n"), 0o644))

	srv := newReadGuardServer(t, repoRoot)

	// Default: the secret value is withheld; the key survives.
	redacted := readFileTool(t, srv, map[string]any{"path": cfgPath})
	require.False(t, redacted.IsError, "config read must not error: %s", readText(t, redacted))
	require.NotContains(t, readText(t, redacted), secretVal, "secret value must be redacted by default")
	require.Contains(t, readText(t, redacted), "token", "the key must survive redaction")

	// allow_secrets serves the value verbatim.
	raw := readFileTool(t, srv, map[string]any{"path": cfgPath, "allow_secrets": true})
	require.False(t, raw.IsError)
	require.Contains(t, readText(t, raw), secretVal, "allow_secrets must serve the value verbatim")
}
