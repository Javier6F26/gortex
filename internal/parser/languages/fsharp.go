package languages

import (
	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// FSharpExtractor extracts F# source using tree-sitter.
type FSharpExtractor struct {
	lang *sitter.Language
}

func NewFSharpExtractor() *FSharpExtractor {
	return &FSharpExtractor{lang: grammars.FsharpLanguage()}
}

func (e *FSharpExtractor) Language() string     { return "fsharp" }
func (e *FSharpExtractor) Extensions() []string { return []string{".fs", ".fsi", ".fsx"} }

func (e *FSharpExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "fsharp",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	add := func(name string, kind graph.NodeKind, start, end int) {
		if name == "" || isFSharpKeyword(name) {
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
			Language: "fsharp",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	walkNodes(root, func(n *sitter.Node) {
		t := parser.NodeType(n, e.lang)
		switch t {
		case "named_module":
			// named_module → long_identifier (e.g. "Math.Shapes")
			name := childText(n, "long_identifier", e.lang, src)
			if name == "" {
				return
			}
			add(name, graph.KindType,
				int(n.StartPoint().Row)+1, int(n.StartPoint().Row)+1)

		case "import_decl":
			name := childText(n, "long_identifier", e.lang, src)
			if name == "" {
				return
			}
			line := int(n.StartPoint().Row) + 1
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: "unresolved::import::" + name,
				Kind: graph.EdgeImports, FilePath: filePath, Line: line,
			})

		case "type_definition":
			// Body is record_type_defn / anon_type_defn / etc. Grab the
			// first type_name → identifier we find.
			name := ""
			walkNodes(n, func(m *sitter.Node) {
				if name != "" {
					return
				}
				if parser.NodeType(m, e.lang) == "type_name" {
					for i := 0; i < int(m.NamedChildCount()); i++ {
						c := m.NamedChild(i)
						if parser.NodeType(c, e.lang) == "identifier" {
							name = c.Text(src)
							return
						}
					}
				}
			})
			add(name, graph.KindType,
				int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1)

		case "function_or_value_defn":
			// value_declaration_left → identifier_pattern →
			// long_identifier_or_op → long_identifier → identifier.
			// Skip when this definition is nested inside a member_defn
			// (wouldn't happen) or inside type_extension_elements where
			// it's a let-binding of a field (e.g. `let mutable count = 0`
			// inside a type) — the original regex captured those too.
			name := firstFSharpDeclName(n, e.lang, src)
			add(name, graph.KindFunction,
				int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1)

		case "member_defn":
			// member_defn → method_or_prop_defn → property_or_ident →
			// last identifier (first is `this` qualifier).
			name := ""
			walkNodes(n, func(m *sitter.Node) {
				if name != "" {
					return
				}
				if parser.NodeType(m, e.lang) != "property_or_ident" {
					return
				}
				var last string
				for i := 0; i < int(m.NamedChildCount()); i++ {
					c := m.NamedChild(i)
					if parser.NodeType(c, e.lang) == "identifier" {
						last = c.Text(src)
					}
				}
				name = last
			})
			add(name, graph.KindMethod,
				int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1)
		}
	})

	return result, nil
}

// firstFSharpDeclName walks a function_or_value_defn and returns the
// first identifier under value_declaration_left.
func firstFSharpDeclName(node *sitter.Node, lang *sitter.Language, src []byte) string {
	var name string
	var pickedLeft bool
	walkNodes(node, func(m *sitter.Node) {
		if name != "" {
			return
		}
		if !pickedLeft {
			if parser.NodeType(m, lang) == "value_declaration_left" {
				pickedLeft = true
				// Dive into identifier_pattern → long_identifier_or_op →
				// long_identifier → identifier and take the first.
				walkNodes(m, func(k *sitter.Node) {
					if name != "" {
						return
					}
					if parser.NodeType(k, lang) == "identifier" {
						name = k.Text(src)
					}
				})
			}
		}
	})
	return name
}

// childText returns the text of the first named child matching a type.
func childText(node *sitter.Node, childType string, lang *sitter.Language, src []byte) string {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		c := node.NamedChild(i)
		if parser.NodeType(c, lang) == childType {
			return c.Text(src)
		}
	}
	return ""
}

func isFSharpKeyword(s string) bool {
	switch s {
	case "let", "and", "in", "if", "then", "else", "elif", "match",
		"with", "when", "for", "while", "do", "done", "begin", "end",
		"fun", "function", "return", "yield", "type", "module",
		"namespace", "open", "member", "static", "rec", "mutable",
		"inline", "private", "internal", "public", "abstract",
		"override", "new", "this", "base", "null", "true", "false",
		"not", "as", "of", "try", "finally", "lazy", "use":
		return true
	}
	return false
}

var _ parser.Extractor = (*FSharpExtractor)(nil)
