package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/indexer"
	"github.com/zzet/gortex/internal/query"
)

// setupMoveInlineRepo materialises a tiny Go module on disk with the given
// per-path content, indexes it through the parser registry, and returns a
// fully-wired MCP Server plus the temp root. The module is named
// `example.com/movetest` so import-path resolution can hit the synthetic
// go.mod.
func setupMoveInlineRepo(t *testing.T, files map[string]string) (*Server, string) {
	t.Helper()
	dir := t.TempDir()

	if _, ok := files["go.mod"]; !ok {
		files["go.mod"] = "module example.com/movetest\n\ngo 1.21\n"
	}

	for rel, content := range files {
		full := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}

	g := graph.New()
	reg := testRegistry()
	cfg := config.Default()
	idx := indexer.New(g, reg, cfg.Index, zap.NewNop())
	_, err := idx.Index(dir)
	require.NoError(t, err)

	eng := query.NewEngine(g)
	srv := NewServer(eng, g, idx, nil, zap.NewNop(), nil)
	srv.RunAnalysis()
	return srv, dir
}

func mustErrText(t *testing.T, result *mcplib.CallToolResult) string {
	t.Helper()
	require.NotEmpty(t, result.Content)
	tc, ok := result.Content[0].(mcplib.TextContent)
	require.True(t, ok, "expected TextContent")
	return tc.Text
}

// ---------------------------------------------------------------------------
// move_symbol
// ---------------------------------------------------------------------------

func TestMoveSymbol_SamePackage_FunctionRelocated(t *testing.T) {
	srv, dir := setupMoveInlineRepo(t, map[string]string{
		"pkga/a.go": `package pkga

// Foo is the function under test.
func Foo() int { return 42 }
`,
		"pkga/b.go": `package pkga

// existing keeps the file non-empty.
func bar() int { return 1 }
`,
	})

	result := callTool(t, srv, "move_symbol", map[string]any{
		"id":          "pkga/a.go::Foo",
		"target_file": "pkga/b.go",
	})
	require.False(t, result.IsError, "result error: %v", result.Content)
	resp := decodeFileOpsResult(t, result)
	assert.Equal(t, true, resp["same_package"])
	assert.Equal(t, "pkga", resp["target_package"])
	assert.Equal(t, float64(0), resp["references_rewritten"])

	// Foo should be gone from a.go and present in b.go.
	aBytes, err := os.ReadFile(filepath.Join(dir, "pkga/a.go"))
	require.NoError(t, err)
	bBytes, err := os.ReadFile(filepath.Join(dir, "pkga/b.go"))
	require.NoError(t, err)
	assert.NotContains(t, string(aBytes), "func Foo()")
	assert.Contains(t, string(bBytes), "func Foo() int { return 42 }")
	assert.Contains(t, string(bBytes), "// Foo is the function under test.")
}

func TestMoveSymbol_CrossPackage_RewritesThirdPartyCaller(t *testing.T) {
	srv, dir := setupMoveInlineRepo(t, map[string]string{
		"pkga/a.go": `package pkga

func Foo() int { return 42 }
`,
		"pkgb/b.go": `package pkgb
`,
		"pkgc/c.go": `package pkgc

import "example.com/movetest/pkga"

func UseFoo() int {
	return pkga.Foo()
}
`,
	})

	result := callTool(t, srv, "move_symbol", map[string]any{
		"id":          "pkga/a.go::Foo",
		"target_file": "pkgb/b.go",
	})
	require.False(t, result.IsError, "result error: %v", result.Content)
	resp := decodeFileOpsResult(t, result)
	assert.Equal(t, false, resp["same_package"])
	assert.GreaterOrEqual(t, resp["references_rewritten"], float64(1))

	cBytes, err := os.ReadFile(filepath.Join(dir, "pkgc/c.go"))
	require.NoError(t, err)
	assert.Contains(t, string(cBytes), "pkgb.Foo()", "caller's qualifier should be rewritten")
	assert.NotContains(t, string(cBytes), "pkga.Foo()", "old qualifier should be removed")
	assert.Contains(t, string(cBytes), `"example.com/movetest/pkgb"`, "new import should be present")
}

func TestMoveSymbol_CrossPackage_CallerInTarget_BecomesBare(t *testing.T) {
	srv, dir := setupMoveInlineRepo(t, map[string]string{
		"pkga/a.go": `package pkga

func Foo() int { return 42 }
`,
		"pkgb/other.go": `package pkgb

import "example.com/movetest/pkga"

func UseFoo() int {
	return pkga.Foo()
}
`,
		"pkgb/b.go": `package pkgb
`,
	})

	result := callTool(t, srv, "move_symbol", map[string]any{
		"id":          "pkga/a.go::Foo",
		"target_file": "pkgb/b.go",
	})
	require.False(t, result.IsError, "result error: %v", result.Content)

	otherBytes, err := os.ReadFile(filepath.Join(dir, "pkgb/other.go"))
	require.NoError(t, err)
	otherSrc := string(otherBytes)
	assert.Contains(t, otherSrc, "Foo()", "callsite should remain")
	assert.NotContains(t, otherSrc, "pkga.Foo()", "qualifier should be stripped")
	assert.NotContains(t, otherSrc, `"example.com/movetest/pkga"`, "import should be dropped once no other pkga usage remains")
}

func TestMoveSymbol_CrossPackage_CallerInSource_GainsImport(t *testing.T) {
	srv, dir := setupMoveInlineRepo(t, map[string]string{
		"pkga/a.go": `package pkga

func Foo() int { return 42 }
`,
		"pkga/other.go": `package pkga

func UseFoo() int {
	return Foo()
}
`,
		"pkgb/b.go": `package pkgb
`,
	})

	result := callTool(t, srv, "move_symbol", map[string]any{
		"id":          "pkga/a.go::Foo",
		"target_file": "pkgb/b.go",
	})
	require.False(t, result.IsError, "result error: %v", result.Content)

	otherBytes, err := os.ReadFile(filepath.Join(dir, "pkga/other.go"))
	require.NoError(t, err)
	otherSrc := string(otherBytes)
	assert.Contains(t, otherSrc, "pkgb.Foo()", "bare call should be qualified with new pkg")
	assert.Contains(t, otherSrc, `"example.com/movetest/pkgb"`, "new import should be added")
}

func TestMoveSymbol_TargetFileMissing_CreatesIt(t *testing.T) {
	srv, dir := setupMoveInlineRepo(t, map[string]string{
		"pkga/a.go": `package pkga

func Foo() int { return 42 }
`,
	})

	result := callTool(t, srv, "move_symbol", map[string]any{
		"id":             "pkga/a.go::Foo",
		"target_file":    "pkgb/new.go",
		"target_package": "pkgb",
	})
	require.False(t, result.IsError, "result error: %v", result.Content)

	got, err := os.ReadFile(filepath.Join(dir, "pkgb/new.go"))
	require.NoError(t, err)
	assert.Contains(t, string(got), "package pkgb")
	assert.Contains(t, string(got), "func Foo() int { return 42 }")
}

func TestMoveSymbol_DryRun_NoOnDiskChanges(t *testing.T) {
	srv, dir := setupMoveInlineRepo(t, map[string]string{
		"pkga/a.go": `package pkga

func Foo() int { return 42 }
`,
		"pkga/b.go": `package pkga

func keep() int { return 1 }
`,
	})

	origA, _ := os.ReadFile(filepath.Join(dir, "pkga/a.go"))
	origB, _ := os.ReadFile(filepath.Join(dir, "pkga/b.go"))

	result := callTool(t, srv, "move_symbol", map[string]any{
		"id":          "pkga/a.go::Foo",
		"target_file": "pkga/b.go",
		"dry_run":     true,
	})
	require.False(t, result.IsError)
	resp := decodeFileOpsResult(t, result)
	assert.Equal(t, true, resp["dry_run"])

	postA, _ := os.ReadFile(filepath.Join(dir, "pkga/a.go"))
	postB, _ := os.ReadFile(filepath.Join(dir, "pkga/b.go"))
	assert.Equal(t, string(origA), string(postA), "dry run must not write the source file")
	assert.Equal(t, string(origB), string(postB), "dry run must not write the target file")
}

func TestMoveSymbol_UnsupportedLanguage(t *testing.T) {
	srv, dir := setupMoveInlineRepo(t, map[string]string{
		"py/foo.py": `def Foo():
    return 42
`,
	})
	// The Python file may not be indexed in this minimal harness; synthesise
	// a Python node directly so the language gate is exercised.
	g := srv.engineFor(t.Context()).Reader()
	_ = g
	// Insert a fake Python symbol directly into the graph.
	pyNode := &graph.Node{
		ID:        "py/foo.py::Foo",
		Kind:      graph.KindFunction,
		Name:      "Foo",
		FilePath:  "py/foo.py",
		StartLine: 1,
		EndLine:   2,
		Language:  "python",
	}
	srv.graph.AddNode(pyNode)
	_ = dir

	result := callTool(t, srv, "move_symbol", map[string]any{
		"id":          "py/foo.py::Foo",
		"target_file": "py/bar.py",
	})
	require.True(t, result.IsError)
	body := mustErrText(t, result)
	assert.Contains(t, body, "unsupported language")
}

// ---------------------------------------------------------------------------
// inline_symbol
// ---------------------------------------------------------------------------

func TestInlineSymbol_TrivialAccessor(t *testing.T) {
	srv, dir := setupMoveInlineRepo(t, map[string]string{
		"pkg/types.go": `package pkg

type S struct {
	X int
}
`,
		"pkg/accessor.go": `package pkg

func GetX(s *S) int { return s.X }
`,
		"pkg/caller.go": `package pkg

func use() int {
	s := &S{X: 3}
	return GetX(s)
}
`,
	})

	result := callTool(t, srv, "inline_symbol", map[string]any{
		"id": "pkg/accessor.go::GetX",
	})
	require.False(t, result.IsError, "result error: %v", result.Content)
	resp := decodeFileOpsResult(t, result)
	assert.GreaterOrEqual(t, resp["callsites_inlined"], float64(1))
	assert.Equal(t, true, resp["callee_deleted"])

	callerBytes, err := os.ReadFile(filepath.Join(dir, "pkg/caller.go"))
	require.NoError(t, err)
	assert.Contains(t, string(callerBytes), "s.X", "callsite should be replaced with body")
	assert.NotContains(t, string(callerBytes), "GetX(s)", "old call should be gone")

	accessorBytes, err := os.ReadFile(filepath.Join(dir, "pkg/accessor.go"))
	require.NoError(t, err)
	assert.NotContains(t, string(accessorBytes), "func GetX", "callee should be deleted")
}

func TestInlineSymbol_TwoCallsites_BothRewritten(t *testing.T) {
	srv, dir := setupMoveInlineRepo(t, map[string]string{
		"pkg/accessor.go": `package pkg

func Double(x int) int { return x + x }
`,
		"pkg/use.go": `package pkg

func a() int { return Double(2) }
func b() int { return Double(5) }
`,
	})

	result := callTool(t, srv, "inline_symbol", map[string]any{
		"id":           "pkg/accessor.go::Double",
		"delete_after": true,
	})
	require.False(t, result.IsError, "result error: %v", result.Content)
	resp := decodeFileOpsResult(t, result)
	assert.Equal(t, float64(2), resp["callsites_inlined"])

	body, err := os.ReadFile(filepath.Join(dir, "pkg/use.go"))
	require.NoError(t, err)
	src := string(body)
	assert.Contains(t, src, "2 + 2")
	assert.Contains(t, src, "5 + 5")
	assert.NotContains(t, src, "Double(")
}

func TestInlineSymbol_ArgumentSubstitution(t *testing.T) {
	srv, dir := setupMoveInlineRepo(t, map[string]string{
		"pkg/add.go": `package pkg

func Add(a, b int) int { return a + b }
`,
		"pkg/use.go": `package pkg

func f(x, y int) int { return Add(x, y) }
`,
	})

	result := callTool(t, srv, "inline_symbol", map[string]any{
		"id": "pkg/add.go::Add",
	})
	require.False(t, result.IsError)
	body, _ := os.ReadFile(filepath.Join(dir, "pkg/use.go"))
	assert.Contains(t, string(body), "x + y")
}

func TestInlineSymbol_SideEffectArgRefuses(t *testing.T) {
	srv, _ := setupMoveInlineRepo(t, map[string]string{
		"pkg/sq.go": `package pkg

func Square(x int) int { return x * x }

func rand() int { return 7 }

func use() int { return Square(rand()) }
`,
	})

	result := callTool(t, srv, "inline_symbol", map[string]any{
		"id": "pkg/sq.go::Square",
	})
	// The tool returns status=refused with refusals[]. We accept either
	// IsError true (with refusal text) or status=refused on a JSON body.
	if result.IsError {
		body := mustErrText(t, result)
		assert.True(t, strings.Contains(body, "refus") || strings.Contains(body, "side effects"),
			"expected refusal mention, got %q", body)
	} else {
		resp := decodeFileOpsResult(t, result)
		assert.Equal(t, "refused", resp["status"])
		refusals, ok := resp["refusals"].([]any)
		require.True(t, ok, "refusals should be a list")
		require.NotEmpty(t, refusals)
	}
}

func TestInlineSymbol_DeleteAfterFalse_KeepsCallee(t *testing.T) {
	srv, dir := setupMoveInlineRepo(t, map[string]string{
		"pkg/sq.go": `package pkg

func GetX(s *Box) int { return s.X }
`,
		"pkg/types.go": `package pkg

type Box struct {
	X int
}
`,
		"pkg/use.go": `package pkg

func u() int { b := &Box{X: 9}; return GetX(b) }
`,
	})

	result := callTool(t, srv, "inline_symbol", map[string]any{
		"id":           "pkg/sq.go::GetX",
		"delete_after": false,
	})
	require.False(t, result.IsError, "result error: %v", result.Content)
	resp := decodeFileOpsResult(t, result)
	assert.Equal(t, false, resp["callee_deleted"])

	sq, _ := os.ReadFile(filepath.Join(dir, "pkg/sq.go"))
	assert.Contains(t, string(sq), "func GetX", "callee should remain when delete_after=false")
	use, _ := os.ReadFile(filepath.Join(dir, "pkg/use.go"))
	assert.Contains(t, string(use), "b.X", "callsite should still be rewritten")
}

func TestInlineSymbol_RefusesDefer(t *testing.T) {
	srv, _ := setupMoveInlineRepo(t, map[string]string{
		"pkg/d.go": `package pkg

import "fmt"

func WithDefer() int {
	defer fmt.Println("done")
	return 1
}

func use() int { return WithDefer() }
`,
	})

	result := callTool(t, srv, "inline_symbol", map[string]any{
		"id": "pkg/d.go::WithDefer",
	})
	require.True(t, result.IsError, "defer-bearing callees must be refused")
	body := mustErrText(t, result)
	assert.True(t,
		strings.Contains(body, "defer") || strings.Contains(body, "single statement"),
		"expected reason mentioning defer or single-statement constraint, got %q", body)
}

func TestInlineSymbol_RefusesMultipleReturns(t *testing.T) {
	srv, _ := setupMoveInlineRepo(t, map[string]string{
		"pkg/m.go": `package pkg

func TwoReturns() (int, int) { return 1, 2 }

func use() (int, int) { return TwoReturns() }
`,
	})

	result := callTool(t, srv, "inline_symbol", map[string]any{
		"id": "pkg/m.go::TwoReturns",
	})
	require.True(t, result.IsError)
	body := mustErrText(t, result)
	assert.True(t,
		strings.Contains(body, "multiple return values") || strings.Contains(body, "single"),
		"expected multi-return refusal, got %q", body)
}

func TestInlineSymbol_UnsupportedLanguage(t *testing.T) {
	srv, _ := setupMoveInlineRepo(t, map[string]string{
		"py/foo.py": `def Foo(): return 1
`,
	})
	pyNode := &graph.Node{
		ID:        "py/foo.py::Foo",
		Kind:      graph.KindFunction,
		Name:      "Foo",
		FilePath:  "py/foo.py",
		StartLine: 1,
		EndLine:   1,
		Language:  "python",
	}
	srv.graph.AddNode(pyNode)

	result := callTool(t, srv, "inline_symbol", map[string]any{
		"id": "py/foo.py::Foo",
	})
	require.True(t, result.IsError)
	body := mustErrText(t, result)
	assert.Contains(t, body, "unsupported language")
}
