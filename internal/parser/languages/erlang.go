package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// ErlangExtractor extracts Erlang source files using tree-sitter.
type ErlangExtractor struct {
	lang *sitter.Language
}

func NewErlangExtractor() *ErlangExtractor {
	return &ErlangExtractor{lang: grammars.ErlangLanguage()}
}

func (e *ErlangExtractor) Language() string     { return "erlang" }
func (e *ErlangExtractor) Extensions() []string { return []string{".erl", ".hrl"} }

func (e *ErlangExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "erlang",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	exported := make(map[string]bool)

	// First pass: attributes (module / export / import / behaviour /
	// type / record) — all live as direct children of source_file.
	for i := 0; i < int(root.ChildCount()); i++ {
		child := root.Child(i)
		if child == nil {
			continue
		}
		switch parser.NodeType(child, e.lang) {
		case "module_attribute":
			name := firstAtomText(child, src, e.lang)
			if name == "" {
				continue
			}
			line := int(child.StartPoint().Row) + 1
			id := filePath + "::" + name
			if seen[id] {
				continue
			}
			seen[id] = true
			result.Nodes = append(result.Nodes, &graph.Node{
				ID: id, Kind: graph.KindPackage, Name: name,
				FilePath: filePath, StartLine: line, EndLine: line,
				Language: "erlang",
			})
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
				FilePath: filePath, Line: line,
			})

		case "export_attribute":
			// Collect exported function names so we can mark them on the
			// function node's Meta.
			walkNodes(child, func(n *sitter.Node) {
				if parser.NodeType(n, e.lang) != "fa" {
					return
				}
				if atom := firstAtomText(n, src, e.lang); atom != "" {
					exported[atom] = true
				}
			})

		case "import_attribute":
			mod := firstAtomText(child, src, e.lang)
			if mod == "" {
				continue
			}
			line := int(child.StartPoint().Row) + 1
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: "unresolved::import::" + mod,
				Kind: graph.EdgeImports, FilePath: filePath, Line: line,
			})

		case "behaviour_attribute":
			name := firstAtomText(child, src, e.lang)
			if name == "" {
				continue
			}
			line := int(child.StartPoint().Row) + 1
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: "unresolved::" + name,
				Kind: graph.EdgeImplements, FilePath: filePath, Line: line,
			})

		case "type_alias", "opaque":
			// type_name → atom
			name := ""
			walkNodes(child, func(n *sitter.Node) {
				if name != "" {
					return
				}
				if parser.NodeType(n, e.lang) == "type_name" {
					name = firstAtomText(n, src, e.lang)
				}
			})
			if name == "" {
				continue
			}
			line := int(child.StartPoint().Row) + 1
			id := filePath + "::" + name
			if seen[id] {
				continue
			}
			seen[id] = true
			result.Nodes = append(result.Nodes, &graph.Node{
				ID: id, Kind: graph.KindType, Name: name,
				FilePath: filePath, StartLine: line, EndLine: line,
				Language: "erlang",
			})
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
				FilePath: filePath, Line: line,
			})

		case "record_decl":
			name := firstAtomText(child, src, e.lang)
			if name == "" {
				continue
			}
			line := int(child.StartPoint().Row) + 1
			id := filePath + "::" + name
			if seen[id] {
				continue
			}
			seen[id] = true
			result.Nodes = append(result.Nodes, &graph.Node{
				ID: id, Kind: graph.KindType, Name: name,
				FilePath: filePath, StartLine: line, EndLine: line,
				Language: "erlang", Meta: map[string]any{"type_kind": "record"},
			})
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
				FilePath: filePath, Line: line,
			})
		}
	}

	// Second pass: function declarations.
	for i := 0; i < int(root.ChildCount()); i++ {
		child := root.Child(i)
		if child == nil || parser.NodeType(child, e.lang) != "fun_decl" {
			continue
		}
		// Pick the first function_clause's atom as the function name.
		name := ""
		walkNodes(child, func(n *sitter.Node) {
			if name != "" {
				return
			}
			if parser.NodeType(n, e.lang) == "function_clause" {
				name = firstAtomText(n, src, e.lang)
			}
		})
		if name == "" {
			continue
		}
		line := int(child.StartPoint().Row) + 1
		endLine := int(child.EndPoint().Row) + 1
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		meta := map[string]any{}
		if exported[name] {
			meta["exported"] = true
		}
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindFunction, Name: name,
			FilePath: filePath, StartLine: line, EndLine: endLine,
			Language: "erlang", Meta: meta,
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: line,
		})
	}

	// Third pass: call edges. Erlang `call` nodes appear inside
	// function bodies. Use the node position to find the enclosing
	// function defined above.
	funcRanges := buildFuncRanges(result)
	walkNodes(root, func(n *sitter.Node) {
		if parser.NodeType(n, e.lang) != "call" {
			return
		}
		// expr → atom (local call) or remote → module:atom (remote call).
		name := ""
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			if c == nil {
				continue
			}
			if parser.NodeType(c, e.lang) == "atom" {
				name = c.Text(src)
				break
			}
		}
		if name == "" || isErlangKeyword(name) {
			return
		}
		line := int(n.StartPoint().Row) + 1
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

// firstAtomText returns the text of the first `atom` descendant.
func firstAtomText(node *sitter.Node, src []byte, lang *sitter.Language) string {
	if node == nil {
		return ""
	}
	var out string
	walkNodes(node, func(n *sitter.Node) {
		if out != "" {
			return
		}
		if parser.NodeType(n, lang) == "atom" {
			out = n.Text(src)
		}
	})
	return out
}

func isErlangKeyword(s string) bool {
	switch s {
	case "if", "case", "of", "end", "fun", "receive", "after",
		"when", "begin", "catch", "try", "throw", "not", "and",
		"or", "band", "bor", "bxor", "bnot", "bsl", "bsr",
		"div", "rem", "let":
		return true
	}
	return false
}

var _ parser.Extractor = (*ErlangExtractor)(nil)
