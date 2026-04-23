package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// OdinExtractor extracts Odin source files.
//
// Odin uses `name :: proc(args) { ... }` for procedures and
// `Name :: struct { ... }` for types. Imports use `import "path"`
// with an optional alias prefix. The odvcencio grammar emits
// `package_declaration`, `import_declaration`, `procedure_declaration`,
// `struct_declaration`, `enum_declaration`, `union_declaration`, and
// `call_expression` for the shapes we care about.
type OdinExtractor struct {
	lang *sitter.Language
}

func NewOdinExtractor() *OdinExtractor {
	return &OdinExtractor{lang: grammars.OdinLanguage()}
}

func (e *OdinExtractor) Language() string     { return "odin" }
func (e *OdinExtractor) Extensions() []string { return []string{".odin"} }

func (e *OdinExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	tree, err := parser.ParseFile(src, e.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	root := tree.RootNode()
	result := &parser.ExtractionResult{}

	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: int(root.EndPoint().Row) + 1,
		Language: "odin",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	add := func(name string, kind graph.NodeKind, start, end int) {
		if name == "" || isOdinKeyword(name) {
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
			Language: "odin",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	// Walk top-level children to collect declarations and imports.
	for i := 0; i < int(root.ChildCount()); i++ {
		child := root.Child(i)
		if child == nil {
			continue
		}
		switch parser.NodeType(child, e.lang) {
		case "package_declaration":
			// package identifier
			name := firstChildOfTypeText(child, "identifier", src, e.lang)
			line := int(child.StartPoint().Row) + 1
			add(name, graph.KindType, line, line)

		case "import_declaration":
			mod := extractOdinImportPath(child, src, e.lang)
			if mod == "" {
				continue
			}
			line := int(child.StartPoint().Row) + 1
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: "unresolved::import::" + mod,
				Kind: graph.EdgeImports, FilePath: filePath, Line: line,
			})

		case "foreign_import_declaration", "foreign_block_declaration":
			// `foreign import name "path"` — capture the path string.
			mod := extractOdinImportPath(child, src, e.lang)
			if mod == "" {
				continue
			}
			line := int(child.StartPoint().Row) + 1
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: "unresolved::import::" + mod,
				Kind: graph.EdgeImports, FilePath: filePath, Line: line,
			})

		case "procedure_declaration":
			name := firstChildOfTypeText(child, "identifier", src, e.lang)
			start := int(child.StartPoint().Row) + 1
			end := int(child.EndPoint().Row) + 1
			add(name, graph.KindFunction, start, end)

		case "struct_declaration", "enum_declaration", "union_declaration":
			name := firstChildOfTypeText(child, "identifier", src, e.lang)
			start := int(child.StartPoint().Row) + 1
			end := int(child.EndPoint().Row) + 1
			add(name, graph.KindType, start, end)
		}
	}

	// Call sites inside procedures.
	funcRanges := buildFuncRanges(result)
	walkNodes(root, func(node *sitter.Node) {
		if parser.NodeType(node, e.lang) != "call_expression" {
			return
		}
		// First child of call_expression is the callee — `identifier`
		// for simple calls, `member_expression` for `math.sqrt`-style.
		// We use the innermost identifier text as the call target.
		var name string
		if node.ChildCount() > 0 {
			callee := node.Child(0)
			if callee != nil {
				switch parser.NodeType(callee, e.lang) {
				case "identifier":
					name = callee.Text(src)
				default:
					// Use last identifier for member access.
					name = lastIdentifierText(callee, src, e.lang)
				}
			}
		}
		if name == "" || isOdinKeyword(name) {
			return
		}
		line := int(node.StartPoint().Row) + 1
		callerID := findEnclosingFunc(funcRanges, line)
		if callerID == "" || strings.HasSuffix(callerID, "::"+name) {
			return
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: callerID, To: "unresolved::" + name,
			Kind: graph.EdgeCalls, FilePath: filePath, Line: line,
		})
	})

	return result, nil
}

// extractOdinImportPath returns the string literal path from an
// import_declaration. Odin represents `"core:fmt"` as a `string` node
// containing a `string_content` child — the content is what we want.
func extractOdinImportPath(node *sitter.Node, src []byte, lang *sitter.Language) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		if parser.NodeType(child, lang) == "string" {
			for j := 0; j < int(child.ChildCount()); j++ {
				inner := child.Child(j)
				if inner != nil && parser.NodeType(inner, lang) == "string_content" {
					return inner.Text(src)
				}
			}
			// Fallback: strip quotes from the full string text.
			return strings.Trim(child.Text(src), `"`)
		}
	}
	return ""
}

// firstChildOfTypeText returns the text of the first direct child whose
// grammar-symbol name matches nodeType.
func firstChildOfTypeText(node *sitter.Node, nodeType string, src []byte, lang *sitter.Language) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child != nil && parser.NodeType(child, lang) == nodeType {
			return child.Text(src)
		}
	}
	return ""
}

// lastIdentifierText returns the text of the last identifier found
// anywhere under node (depth-first).
func lastIdentifierText(node *sitter.Node, src []byte, lang *sitter.Language) string {
	var last string
	walkNodes(node, func(n *sitter.Node) {
		if parser.NodeType(n, lang) == "identifier" {
			last = n.Text(src)
		}
	})
	return last
}

func isOdinKeyword(s string) bool {
	switch s {
	case "if", "else", "for", "switch", "case", "break", "continue",
		"return", "defer", "when", "in", "not_in", "do",
		"proc", "struct", "enum", "union", "bit_set", "map", "distinct",
		"package", "import", "foreign", "using", "where",
		"true", "false", "nil", "or_else", "or_return":
		return true
	}
	return false
}

var _ parser.Extractor = (*OdinExtractor)(nil)
