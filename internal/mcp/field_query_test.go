package mcp

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func TestParseFieldQuery(t *testing.T) {
	cases := []struct {
		raw  string
		want fieldQuery
	}{
		{"validate token", fieldQuery{Text: "validate token"}},
		{"kind:function auth", fieldQuery{Text: "auth", Kind: "function"}},
		{"lang:rust path:src/ parse", fieldQuery{Text: "parse", Lang: "rust", Path: "src/"}},
		{"repo:gortex project:web Handler", fieldQuery{Text: "Handler", Repo: "gortex", Project: "web"}},
		{"language:go kind:method,function run", fieldQuery{Text: "run", Lang: "go", Kind: "method,function"}},
		// A non-field token with a colon stays in the free text.
		{"pkg::Type lookup", fieldQuery{Text: "pkg::Type lookup"}},
		{"https://example.com client", fieldQuery{Text: "https://example.com client"}},
		// An unknown field name is left in the text verbatim.
		{"author:alice fix", fieldQuery{Text: "author:alice fix"}},
		// A field with an empty value is treated as plain text.
		{"kind: thing", fieldQuery{Text: "kind: thing"}},
		// A repeated field keeps the last value.
		{"kind:function kind:method x", fieldQuery{Text: "x", Kind: "method"}},
	}
	for _, tc := range cases {
		got := parseFieldQuery(tc.raw)
		if got != tc.want {
			t.Errorf("parseFieldQuery(%q) = %+v, want %+v", tc.raw, got, tc.want)
		}
	}
}

func TestNormalizeLang(t *testing.T) {
	cases := map[string]string{
		"ts":         "typescript",
		"JS":         "javascript",
		" py ":       "python",
		"rs":         "rust",
		"go":         "go",
		"typescript": "typescript",
		"":           "",
	}
	for in, want := range cases {
		if got := normalizeLang(in); got != want {
			t.Errorf("normalizeLang(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFieldQueryHasFieldFilters(t *testing.T) {
	if (fieldQuery{Text: "x"}).hasFieldFilters() {
		t.Errorf("plain text query must report no field filters")
	}
	if (fieldQuery{Project: "web"}).hasFieldFilters() {
		t.Errorf("project: alone is scope, not a post-filter")
	}
	for _, fq := range []fieldQuery{{Kind: "function"}, {Lang: "go"}, {Path: "src/"}, {Repo: "gortex"}} {
		if !fq.hasFieldFilters() {
			t.Errorf("%+v must report a field filter", fq)
		}
	}
}

func TestApplyFieldFilters(t *testing.T) {
	nodes := []*graph.Node{
		{ID: "a", Language: "go", FilePath: "internal/auth/token.go", RepoPrefix: "gortex"},
		{ID: "b", Language: "rust", FilePath: "src/main.rs", RepoPrefix: "gortex"},
		{ID: "c", Language: "go", FilePath: "cmd/main.go", RepoPrefix: "cloud"},
	}
	ids := func(ns []*graph.Node) []string {
		out := make([]string, len(ns))
		for i, n := range ns {
			out[i] = n.ID
		}
		return out
	}

	if got := ids(applyFieldFilters(nodes, fieldQuery{Lang: "go"})); !equalStrings(got, []string{"a", "c"}) {
		t.Errorf("lang:go = %v, want [a c]", got)
	}
	if got := ids(applyFieldFilters(nodes, fieldQuery{Lang: "rs"})); !equalStrings(got, []string{"b"}) {
		t.Errorf("lang:rs (alias) = %v, want [b]", got)
	}
	if got := ids(applyFieldFilters(nodes, fieldQuery{Path: "internal/"})); !equalStrings(got, []string{"a"}) {
		t.Errorf("path:internal/ = %v, want [a]", got)
	}
	if got := ids(applyFieldFilters(nodes, fieldQuery{Repo: "cloud"})); !equalStrings(got, []string{"c"}) {
		t.Errorf("repo:cloud = %v, want [c]", got)
	}
	if got := ids(applyFieldFilters(nodes, fieldQuery{Lang: "go", Path: "cmd/"})); !equalStrings(got, []string{"c"}) {
		t.Errorf("lang:go path:cmd/ = %v, want [c]", got)
	}
	if got := applyFieldFilters(nodes, fieldQuery{}); len(got) != 3 {
		t.Errorf("no clauses must keep all nodes, got %d", len(got))
	}
}

// TestNameClauseFilter proves the name: clause both parses out of the query and
// post-filters nodes by a case-insensitive substring of the symbol name —
// "search for X but only nodes whose name contains Y".
func TestNameClauseFilter(t *testing.T) {
	fq := parseFieldQuery("auth name:Handler kind:function")
	if fq.Name != "Handler" {
		t.Errorf("name clause not parsed: fq.Name=%q, want Handler", fq.Name)
	}
	if fq.Kind != "function" {
		t.Errorf("kind clause lost: fq.Kind=%q", fq.Kind)
	}
	if fq.Text != "auth" {
		t.Errorf("residual text=%q, want auth", fq.Text)
	}
	if !fq.hasFieldFilters() {
		t.Error("hasFieldFilters should be true when name: is set")
	}

	nodes := []*graph.Node{
		{ID: "a", Name: "AuthHandler", Kind: graph.KindType},
		{ID: "b", Name: "authMiddleware", Kind: graph.KindFunction},
		{ID: "c", Name: "RequestHandler", Kind: graph.KindType},
	}
	got := applyFieldFilters(nodes, fieldQuery{Name: "handler"})
	ids := map[string]bool{}
	for _, n := range got {
		ids[n.ID] = true
	}
	if !ids["a"] || !ids["c"] {
		t.Errorf("name:handler should keep AuthHandler + RequestHandler, got %v", ids)
	}
	if ids["b"] {
		t.Error("name:handler should have dropped authMiddleware")
	}
}
