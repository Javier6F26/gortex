package languages

import (
	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// The D language is C-like and brace-delimited. It has the full
// ML-ish aggregate set (struct/class/interface/enum/union/template),
// plus a `module` statement that names the unit (not a symbol).
//
// The odvcencio D grammar exposes:
//   source_file → module_def
//     module_declaration(module, module_fqn)
//     import_declaration(import, imported → module_fqn)
//     struct_declaration / class_declaration / interface_declaration /
//     enum_declaration / union_declaration / template_declaration
//     function_declaration(type, identifier, parameters, function_body)
//     constructor(this, parameters, function_body)

// DExtractor extracts D-language source files into graph nodes and edges.
type DExtractor struct {
	lang *sitter.Language
}

func NewDExtractor() *DExtractor {
	return &DExtractor{lang: grammars.DLanguage()}
}

func (e *DExtractor) Language() string     { return "d" }
func (e *DExtractor) Extensions() []string { return []string{".d", ".di"} }

func (e *DExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "d",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	add := func(name string, kind graph.NodeKind, start, end int) {
		if name == "" || dIsKeyword(name) {
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
			Language: "d",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	firstIdent := func(node *sitter.Node) string {
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			if child != nil && parser.NodeType(child, e.lang) == "identifier" {
				return child.Text(src)
			}
		}
		return ""
	}

	walkNodes(root, func(node *sitter.Node) {
		t := parser.NodeType(node, e.lang)
		startLine := int(node.StartPoint().Row) + 1
		endLine := int(node.EndPoint().Row) + 1

		switch t {
		case "struct_declaration", "class_declaration", "enum_declaration",
			"union_declaration", "template_declaration":
			add(firstIdent(node), graph.KindType, startLine, endLine)

		case "interface_declaration":
			add(firstIdent(node), graph.KindInterface, startLine, endLine)

		case "function_declaration":
			add(firstIdent(node), graph.KindFunction, startLine, endLine)

		case "import_declaration":
			// Each `imported` child holds a module_fqn.
			for i := 0; i < int(node.ChildCount()); i++ {
				child := node.Child(i)
				if child == nil || parser.NodeType(child, e.lang) != "imported" {
					continue
				}
				for j := 0; j < int(child.ChildCount()); j++ {
					gc := child.Child(j)
					if gc == nil {
						continue
					}
					if parser.NodeType(gc, e.lang) == "module_fqn" {
						mod := gc.Text(src)
						result.Edges = append(result.Edges, &graph.Edge{
							From: fileNode.ID, To: "unresolved::import::" + mod,
							Kind: graph.EdgeImports, FilePath: filePath, Line: startLine,
						})
					}
				}
			}
		}
	})

	return result, nil
}

func dIsKeyword(s string) bool {
	switch s {
	case "if", "else", "while", "for", "foreach", "do", "switch", "case",
		"default", "return", "break", "continue", "struct", "class",
		"interface", "enum", "union", "template", "import", "module",
		"public", "private", "protected", "package", "static", "final",
		"override", "pure", "nothrow", "extern", "export", "pragma",
		"void", "int", "uint", "long", "ulong", "short", "ushort",
		"byte", "ubyte", "float", "double", "real", "bool", "char",
		"wchar", "dchar", "string", "auto", "const", "immutable",
		"shared", "ref", "in", "out", "inout", "true", "false", "null":
		return true
	}
	return false
}

var _ parser.Extractor = (*DExtractor)(nil)
