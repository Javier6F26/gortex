package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// LiquidExtractor extracts Shopify/Jekyll Liquid templates using tree-sitter.
//
// Relevant odvcencio grammar nodes:
//   assignment_statement ("assign" identifier "=" value)
//   capture_statement    ("capture" identifier %} block {% "endcapture")
//   include_statement    ("include" string)
//   render_statement     ("render" string)
//
// All of these are top-level children of the `program` root.
type LiquidExtractor struct {
	lang *sitter.Language
}

func NewLiquidExtractor() *LiquidExtractor {
	return &LiquidExtractor{lang: grammars.LiquidLanguage()}
}

func (e *LiquidExtractor) Language() string     { return "liquid" }
func (e *LiquidExtractor) Extensions() []string { return []string{".liquid"} }

func (e *LiquidExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	tree, err := parser.ParseFile(src, e.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	root := tree.RootNode()
	result := &parser.ExtractionResult{}

	lineCount := strings.Count(string(src), "\n") + 1
	if lineCount < 1 {
		lineCount = 1
	}
	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: lineCount,
		Language: "liquid",
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
			Language: "liquid",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	// Helper: grab the first direct identifier child's text.
	firstIdent := func(n *sitter.Node) string {
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			if c != nil && parser.NodeType(c, e.lang) == "identifier" {
				return c.Text(src)
			}
		}
		return ""
	}

	// Helper: extract the quoted text of the first `string` child.
	firstString := func(n *sitter.Node) string {
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			if c == nil {
				continue
			}
			if parser.NodeType(c, e.lang) == "string" {
				return strings.Trim(c.Text(src), `"'`)
			}
		}
		return ""
	}

	walkNodes(root, func(n *sitter.Node) {
		start := int(n.StartPoint().Row) + 1
		end := int(n.EndPoint().Row) + 1

		switch parser.NodeType(n, e.lang) {
		case "assignment_statement":
			name := firstIdent(n)
			add(name, graph.KindVariable, start, start)

		case "capture_statement":
			name := firstIdent(n)
			add(name, graph.KindFunction, start, end)

		case "include_statement", "render_statement":
			mod := firstString(n)
			if mod == "" {
				return
			}
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: "unresolved::import::" + mod,
				Kind: graph.EdgeImports, FilePath: filePath, Line: start,
			})
		}
	})

	return result, nil
}

var _ parser.Extractor = (*LiquidExtractor)(nil)
