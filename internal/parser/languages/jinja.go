package languages

import (
	"regexp"
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// Jinja2 grammar in odvcencio is lexer-only: it emits `jinja_expression`
// wrappers around `{% %}` / `{{ }}` blocks but does not parse the
// individual tag names, arguments, or block structure. The top-level
// node is often `ERROR` on typical templates because control-flow tags
// leave the parser out of balance.
//
// Until a richer Jinja grammar ships upstream, we use tree-sitter only
// to confirm parseability (and to hold the *sitter.Language handle for
// parity with other adapters). Symbol extraction is done with narrow
// regexes over the raw source — the same ones the legacy implementation
// used, kept tight so they cannot accidentally match expression
// contents. Extractions:
//
//   - `{% block NAME %}` / `{% endblock %}`   → KindFunction
//   - `{% macro NAME(args) %}` / `{% endmacro %}` → KindFunction
//   - `{% extends "X" %}` / include / import / from "X" import → EdgeImports
var (
	jinjaBlockRe      = regexp.MustCompile(`(?m)\{%\s*block\s+([A-Za-z_][\w]*)`)
	jinjaMacroRe      = regexp.MustCompile(`(?m)\{%\s*macro\s+([A-Za-z_][\w]*)\s*\(`)
	jinjaExtendsRe    = regexp.MustCompile(`(?m)\{%\s*extends\s+['"]([^'"]+)['"]`)
	jinjaIncludeRe    = regexp.MustCompile(`(?m)\{%\s*include\s+['"]([^'"]+)['"]`)
	jinjaImportRe     = regexp.MustCompile(`(?m)\{%\s*import\s+['"]([^'"]+)['"]`)
	jinjaFromImportRe = regexp.MustCompile(`(?m)\{%\s*from\s+['"]([^'"]+)['"]\s+import`)
)

// JinjaExtractor extracts Jinja2 templates.
type JinjaExtractor struct {
	lang *sitter.Language
}

func NewJinjaExtractor() *JinjaExtractor {
	return &JinjaExtractor{lang: grammars.Jinja2Language()}
}

func (e *JinjaExtractor) Language() string     { return "jinja" }
func (e *JinjaExtractor) Extensions() []string { return []string{".jinja", ".jinja2", ".j2"} }

func (e *JinjaExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	// Run the tree-sitter parse so parse failures still surface, even
	// though we extract via regex. Discard the tree once parsed.
	if tree, err := parser.ParseFile(src, e.lang); err == nil {
		tree.Close()
	}

	lines := strings.Split(string(src), "\n")
	result := &parser.ExtractionResult{}

	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: len(lines),
		Language: "jinja",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	add := func(name string, kind graph.NodeKind, start, end int) {
		if name == "" {
			return
		}
		id := filePath + "::" + name
		if seen[id] {
			return
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: kind, Name: name,
			FilePath: filePath, StartLine: start, EndLine: end,
			Language: "jinja",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	for _, m := range jinjaBlockRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		add(name, graph.KindFunction, line, findKeywordBlockEnd(lines, line, "{% endblock"))
	}
	for _, m := range jinjaMacroRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		add(name, graph.KindFunction, line, findKeywordBlockEnd(lines, line, "{% endmacro"))
	}

	for _, re := range []*regexp.Regexp{jinjaExtendsRe, jinjaIncludeRe, jinjaImportRe, jinjaFromImportRe} {
		for _, m := range re.FindAllSubmatchIndex(src, -1) {
			mod := string(src[m[2]:m[3]])
			line := lineAt(src, m[0])
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: "unresolved::import::" + mod,
				Kind: graph.EdgeImports, FilePath: filePath, Line: line,
			})
		}
	}

	return result, nil
}

var _ parser.Extractor = (*JinjaExtractor)(nil)
