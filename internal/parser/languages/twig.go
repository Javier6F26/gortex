package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// Twig is Symfony's templating language and shares a tag vocabulary
// with Jinja: `{% block %}`, `{% macro %}`, `{% extends %}`,
// `{% include %}`, `{% import %}`. The odvcencio grammar represents
// directives as `statement_directive` wrappers around `tag_statement`,
// `import_statement`, and `macro_statement` forms. We walk the tree
// once, branch on the inner form, and surface the same graph shape as
// the previous regex-based implementation.

// TwigExtractor extracts Symfony Twig templates.
type TwigExtractor struct {
	lang *sitter.Language
}

func NewTwigExtractor() *TwigExtractor {
	return &TwigExtractor{lang: grammars.TwigLanguage()}
}

func (e *TwigExtractor) Language() string     { return "twig" }
func (e *TwigExtractor) Extensions() []string { return []string{".twig"} }

func (e *TwigExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	tree, err := parser.ParseFile(src, e.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	root := tree.RootNode()
	lines := strings.Split(string(src), "\n")
	result := &parser.ExtractionResult{}

	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: len(lines),
		Language: "twig",
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
			Language: "twig",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	addImport := func(target string, line int) {
		if target == "" {
			return
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + target,
			Kind: graph.EdgeImports, FilePath: filePath, Line: line,
		})
	}

	walkNodes(root, func(node *sitter.Node) {
		switch parser.NodeType(node, e.lang) {
		case "tag_statement":
			tag := e.childText(node, "tag", src)
			startLine := int(node.StartPoint().Row) + 1
			switch tag {
			case "block":
				name := e.firstChildByType(node, "variable", src)
				if name == "" {
					// Some grammars expose it as 'name' or 'identifier'.
					name = e.firstChildByType(node, "name", src)
				}
				if name == "" {
					name = e.firstChildByType(node, "identifier", src)
				}
				add(name, graph.KindFunction,
					startLine, findKeywordBlockEnd(lines, startLine, "{% endblock"))
			case "extends", "include", "use", "from":
				target := e.extractStringTarget(node, src)
				addImport(target, startLine)
			}
		case "macro_statement":
			name := e.firstChildByType(node, "method", src)
			if name == "" {
				name = e.firstChildByType(node, "name", src)
			}
			if name == "" {
				name = e.firstChildByType(node, "identifier", src)
			}
			startLine := int(node.StartPoint().Row) + 1
			add(name, graph.KindFunction,
				startLine, findKeywordBlockEnd(lines, startLine, "{% endmacro"))
		case "import_statement":
			target := e.extractStringTarget(node, src)
			addImport(target, int(node.StartPoint().Row)+1)
		}
	})

	return result, nil
}

// childText returns the text of the first direct child whose grammar
// type matches the given name.
func (e *TwigExtractor) childText(node *sitter.Node, typ string, src []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, e.lang) == typ {
			return c.Text(src)
		}
	}
	return ""
}

// firstChildByType returns the text of the first direct child whose
// grammar type matches, but also accepts nested lookups for named
// captures like `variable`/`method`.
func (e *TwigExtractor) firstChildByType(node *sitter.Node, typ string, src []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, e.lang) == typ {
			return strings.TrimSpace(c.Text(src))
		}
	}
	return ""
}

// extractStringTarget pulls the template path out of an
// interpolated_string child (the quoted argument of extends/include/
// import). The grammar doesn't emit a dedicated string_content node
// inside interpolated_string for plain literals, so we strip the outer
// quotes ourselves.
func (e *TwigExtractor) extractStringTarget(node *sitter.Node, src []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		t := parser.NodeType(c, e.lang)
		if t != "interpolated_string" && t != "string" {
			continue
		}
		raw := c.Text(src)
		raw = strings.TrimSpace(raw)
		raw = strings.Trim(raw, "\"'")
		return raw
	}
	return ""
}

var _ parser.Extractor = (*TwigExtractor)(nil)
