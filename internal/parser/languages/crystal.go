package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// Crystal shares most of Ruby's surface syntax plus static types.
// `def` for methods, `class` / `module` / `struct` for types,
// `require "path"` for dependencies.
//
// The odvcencio Crystal grammar exposes:
//   source_file
//     require(string)
//     module_def(constant, …)
//     class_def(constant, method_def, …)
//     struct_def / enum_def
//     method_def(identifier | self . identifier, …)
//     call(identifier | constant . identifier, argument_list)

// CrystalExtractor extracts Crystal source files into graph nodes and edges.
type CrystalExtractor struct {
	lang *sitter.Language
}

func NewCrystalExtractor() *CrystalExtractor {
	return &CrystalExtractor{lang: grammars.CrystalLanguage()}
}

func (e *CrystalExtractor) Language() string     { return "crystal" }
func (e *CrystalExtractor) Extensions() []string { return []string{".cr"} }

func (e *CrystalExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "crystal",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	add := func(name string, kind graph.NodeKind, start, end int) {
		if name == "" || isCrystalKeyword(name) {
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
			Language: "crystal",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	// Defs and requires — walk entire tree.
	walkNodes(root, func(node *sitter.Node) {
		t := parser.NodeType(node, e.lang)
		startLine := int(node.StartPoint().Row) + 1
		endLine := int(node.EndPoint().Row) + 1

		switch t {
		case "require":
			// require (string "...")
			for i := 0; i < int(node.ChildCount()); i++ {
				child := node.Child(i)
				if child != nil && parser.NodeType(child, e.lang) == "string" {
					mod := strings.Trim(child.Text(src), `"'`)
					if mod == "" {
						break
					}
					result.Edges = append(result.Edges, &graph.Edge{
						From: fileNode.ID, To: "unresolved::import::" + mod,
						Kind: graph.EdgeImports, FilePath: filePath, Line: startLine,
					})
					break
				}
			}

		case "module_def", "class_def", "struct_def", "enum_def", "lib_def":
			// First `constant` child is the type name.
			for i := 0; i < int(node.ChildCount()); i++ {
				child := node.Child(i)
				if child != nil && parser.NodeType(child, e.lang) == "constant" {
					name := child.Text(src)
					add(name, graph.KindType, startLine, endLine)
					break
				}
			}

		case "method_def", "abstract_method_def":
			// def <identifier> or def self . <identifier>
			name := ""
			for i := 0; i < int(node.ChildCount()); i++ {
				child := node.Child(i)
				if child == nil {
					continue
				}
				if parser.NodeType(child, e.lang) == "identifier" {
					name = child.Text(src)
					break
				}
			}
			add(name, graph.KindMethod, startLine, endLine)
		}
	})

	// Call sites.
	funcRanges := buildFuncRanges(result)
	walkNodes(root, func(node *sitter.Node) {
		if parser.NodeType(node, e.lang) != "call" {
			return
		}
		line := int(node.StartPoint().Row) + 1
		callerID := findEnclosingFunc(funcRanges, line)
		if callerID == "" {
			return
		}

		// The callee is the last `identifier` before the argument_list.
		// In `Server.new(8080).start` the outer call.callee is `.start` (identifier=start),
		// and the inner call has callee identifier=new.
		calleeName := ""
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			if child == nil {
				continue
			}
			ct := parser.NodeType(child, e.lang)
			if ct == "identifier" {
				calleeName = child.Text(src)
			}
		}
		if calleeName == "" || isCrystalKeyword(calleeName) {
			return
		}
		if strings.HasSuffix(callerID, "::"+calleeName) {
			return
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: callerID, To: "unresolved::" + calleeName,
			Kind: graph.EdgeCalls, FilePath: filePath, Line: line,
		})
	})

	return result, nil
}

func isCrystalKeyword(s string) bool {
	switch s {
	case "if", "elsif", "else", "unless", "while", "until", "for", "case",
		"when", "in", "then", "do", "end", "begin", "rescue", "ensure",
		"raise", "return", "next", "break", "yield", "def", "class", "module",
		"struct", "abstract", "private", "protected", "public", "self",
		"true", "false", "nil", "require", "include", "extend":
		return true
	}
	return false
}

var _ parser.Extractor = (*CrystalExtractor)(nil)
