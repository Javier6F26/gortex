package mcp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"testing"
)

// TestAnalyzeKinds_MatchesSwitch is an anti-drift guard: it parses
// tools_enhancements.go, locates the handleAnalyze method, walks its
// dispatch switch, and asserts the set of `case` string literals equals
// analyzeKinds exactly. If a new `case "<kind>":` is added to the switch
// without updating analyzeKinds (or vice versa), this fails — keeping
// the canonical kind list, the two error strings, and the tool
// description from going stale.
func TestAnalyzeKinds_MatchesSwitch(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "tools_enhancements.go", nil, 0)
	if err != nil {
		t.Fatalf("parse tools_enhancements.go: %v", err)
	}

	switchCases := collectAnalyzeSwitchCases(t, file)
	if len(switchCases) == 0 {
		t.Fatal("found no case labels in the handleAnalyze switch — parser walk is broken")
	}

	want := make(map[string]bool, len(switchCases))
	for _, c := range switchCases {
		want[c] = true
	}
	have := make(map[string]bool, len(analyzeKinds))
	for _, k := range analyzeKinds {
		have[k] = true
	}

	for c := range want {
		if !have[c] {
			t.Errorf("switch case %q is missing from analyzeKinds", c)
		}
	}
	for k := range have {
		if !want[k] {
			t.Errorf("analyzeKinds entry %q has no matching switch case", k)
		}
	}
}

// collectAnalyzeSwitchCases finds func (s *Server) handleAnalyze and
// returns every string literal used as a `case` expression in its body's
// switch statement (flattening multi-value cases like `case "sast",
// "hygiene":`).
func collectAnalyzeSwitchCases(t *testing.T, file *ast.File) []string {
	t.Helper()
	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "handleAnalyze" || fd.Recv == nil {
			continue
		}
		fn = fd
		break
	}
	if fn == nil {
		t.Fatal("handleAnalyze function not found in tools_enhancements.go")
	}

	var cases []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		for _, stmt := range sw.Body.List {
			cc, ok := stmt.(*ast.CaseClause)
			if !ok || cc.List == nil { // default clause has nil List
				continue
			}
			for _, expr := range cc.List {
				lit, ok := expr.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				val, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("unquote case literal %q: %v", lit.Value, err)
				}
				cases = append(cases, val)
			}
		}
		return true
	})
	return cases
}

// TestAnalyzeKinds_SortedDefensiveCopy asserts AnalyzeKinds returns a
// sorted copy that callers may mutate without corrupting the package
// source.
func TestAnalyzeKinds_SortedDefensiveCopy(t *testing.T) {
	got := AnalyzeKinds()
	if len(got) != len(analyzeKinds) {
		t.Fatalf("AnalyzeKinds len = %d, want %d", len(got), len(analyzeKinds))
	}

	if !sort.StringsAreSorted(got) {
		t.Errorf("AnalyzeKinds() is not sorted: %v", got)
	}
	if !sort.StringsAreSorted(analyzeKinds) {
		t.Errorf("analyzeKinds source slice is not sorted")
	}

	// Mutating the returned slice must not affect the package source.
	orig := analyzeKinds[0]
	got[0] = "zzz_mutated_sentinel"
	if analyzeKinds[0] != orig {
		t.Errorf("AnalyzeKinds did not return a defensive copy: source mutated to %q", analyzeKinds[0])
	}
}
