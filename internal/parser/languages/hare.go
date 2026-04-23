package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// HareExtractor extracts Hare source files using tree-sitter.
//
// Hare grammar shape (odvcencio):
//   module
//     imports
//       use_statement ("use" identifier|scoped_type_identifier ";")
//     declarations
//       type_declaration ("type" identifier "=" struct_type|enum_type|union_type ...)
//       function_declaration ("fn" identifier "(" parameter* ")" ... block)
//         block
//           call_expression → identifier | scoped_type_identifier
type HareExtractor struct {
	lang *sitter.Language
}

func NewHareExtractor() *HareExtractor {
	return &HareExtractor{lang: grammars.HareLanguage()}
}

func (e *HareExtractor) Language() string     { return "hare" }
func (e *HareExtractor) Extensions() []string { return []string{".ha"} }

func (e *HareExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	tree, err := parser.ParseFile(src, e.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	root := tree.RootNode()
	result := &parser.ExtractionResult{}

	lines := strings.Count(string(src), "\n") + 1
	if lines < 1 {
		lines = 1
	}
	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: lines,
		Language: "hare",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	add := func(name string, kind graph.NodeKind, start, end int) {
		if name == "" || isHareKeyword(name) {
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
			Language: "hare",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	// Walk for declarations and imports.
	walkNodes(root, func(n *sitter.Node) {
		switch parser.NodeType(n, e.lang) {
		case "use_statement":
			e.extractUse(n, src, fileNode, filePath, result)
		case "type_declaration":
			name, line := firstIdentifierChild(n, src, e.lang)
			if name != "" {
				end := int(n.EndPoint().Row) + 1
				add(name, graph.KindType, line, end)
			}
		case "function_declaration":
			name, line := firstIdentifierChild(n, src, e.lang)
			if name != "" {
				end := int(n.EndPoint().Row) + 1
				add(name, graph.KindFunction, line, end)
			}
		}
	})

	// Call sites inside functions.
	funcRanges := buildFuncRanges(result)
	walkNodes(root, func(n *sitter.Node) {
		if parser.NodeType(n, e.lang) != "call_expression" {
			return
		}
		line := int(n.StartPoint().Row) + 1
		callerID := findEnclosingFunc(funcRanges, line)
		if callerID == "" {
			return
		}
		// The callee is the first child; it may be identifier or scoped_type_identifier.
		if n.ChildCount() == 0 {
			return
		}
		callee := n.Child(0)
		if callee == nil {
			return
		}
		var name string
		switch parser.NodeType(callee, e.lang) {
		case "identifier":
			name = callee.Text(src)
		case "scoped_type_identifier", "scoped_identifier":
			// Take the last identifier component.
			for i := int(callee.ChildCount()) - 1; i >= 0; i-- {
				c := callee.Child(i)
				if c != nil && parser.NodeType(c, e.lang) == "identifier" {
					name = c.Text(src)
					break
				}
			}
		}
		if name == "" || isHareKeyword(name) {
			return
		}
		if strings.HasSuffix(callerID, "::"+name) {
			return
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: callerID, To: "unresolved::" + name,
			Kind: graph.EdgeCalls, FilePath: filePath, Line: line,
		})
	})

	return result, nil
}

// extractUse handles `use X;` and `use X::Y;` import statements.
func (e *HareExtractor) extractUse(
	node *sitter.Node, src []byte, fileNode *graph.Node, filePath string,
	result *parser.ExtractionResult,
) {
	line := int(node.StartPoint().Row) + 1
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		switch parser.NodeType(child, e.lang) {
		case "identifier":
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: "unresolved::import::" + child.Text(src),
				Kind: graph.EdgeImports, FilePath: filePath, Line: line,
			})
			return
		case "scoped_type_identifier", "scoped_identifier":
			// Join segments with "::".
			var parts []string
			for j := 0; j < int(child.ChildCount()); j++ {
				cc := child.Child(j)
				if cc != nil && parser.NodeType(cc, e.lang) == "identifier" {
					parts = append(parts, cc.Text(src))
				}
			}
			if len(parts) > 0 {
				result.Edges = append(result.Edges, &graph.Edge{
					From: fileNode.ID, To: "unresolved::import::" + strings.Join(parts, "::"),
					Kind: graph.EdgeImports, FilePath: filePath, Line: line,
				})
			}
			return
		}
	}
}

// firstIdentifierChild returns the name and start line of the first
// direct `identifier` child of node, or ("", 0) if none.
func firstIdentifierChild(node *sitter.Node, src []byte, lang *sitter.Language) (string, int) {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, lang) == "identifier" {
			return c.Text(src), int(node.StartPoint().Row) + 1
		}
	}
	return "", 0
}

func isHareKeyword(s string) bool {
	switch s {
	case "if", "else", "for", "switch", "match", "case", "break", "continue",
		"return", "defer", "yield", "abort", "assert",
		"fn", "type", "struct", "union", "enum", "const", "let",
		"use", "export", "static", "nullable", "const_fn",
		"true", "false", "null", "void", "as", "is":
		return true
	}
	return false
}

var _ parser.Extractor = (*HareExtractor)(nil)
