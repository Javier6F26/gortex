package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// FortranExtractor extracts Fortran source using tree-sitter.
type FortranExtractor struct {
	lang *sitter.Language
}

func NewFortranExtractor() *FortranExtractor {
	return &FortranExtractor{lang: grammars.FortranLanguage()}
}

func (e *FortranExtractor) Language() string { return "fortran" }
func (e *FortranExtractor) Extensions() []string {
	return []string{".f", ".F", ".for", ".FOR", ".ftn", ".f90", ".F90", ".f95", ".F95", ".f03", ".F03", ".f08", ".F08"}
}

func (e *FortranExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "fortran",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	add := func(name string, kind graph.NodeKind, start, end int, meta map[string]any) {
		if name == "" || isFortranKeyword(strings.ToLower(name)) {
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
			Language: "fortran",
			Meta:     meta,
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	// The Fortran grammar wraps everything in `translation_unit` and uses
	// distinct nodes for each construct. Walk the tree; each declaration
	// type has a `name` child that carries the identifier text.
	walkNodes(root, func(n *sitter.Node) {
		t := parser.NodeType(n, e.lang)
		switch t {
		case "module":
			name := fortranHeaderName(n, "module_statement", e.lang, src)
			add(name, graph.KindType,
				int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1,
				map[string]any{"fortran_kind": "module"})

		case "program":
			name := fortranHeaderName(n, "program_statement", e.lang, src)
			add(name, graph.KindFunction,
				int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1,
				map[string]any{"fortran_kind": "program"})

		case "function":
			name := fortranHeaderName(n, "function_statement", e.lang, src)
			add(name, graph.KindFunction,
				int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1,
				map[string]any{"fortran_kind": "function"})

		case "subroutine":
			name := fortranHeaderName(n, "subroutine_statement", e.lang, src)
			add(name, graph.KindFunction,
				int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1,
				map[string]any{"fortran_kind": "subroutine"})

		case "derived_type_definition":
			// derived_type_statement → type_name
			name := ""
			for i := 0; i < int(n.NamedChildCount()); i++ {
				c := n.NamedChild(i)
				if parser.NodeType(c, e.lang) == "derived_type_statement" {
					for j := 0; j < int(c.NamedChildCount()); j++ {
						g := c.NamedChild(j)
						if parser.NodeType(g, e.lang) == "type_name" {
							name = g.Text(src)
							break
						}
					}
					break
				}
			}
			add(name, graph.KindType,
				int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, nil)

		case "use_statement":
			mod := ""
			for i := 0; i < int(n.NamedChildCount()); i++ {
				c := n.NamedChild(i)
				if parser.NodeType(c, e.lang) == "module_name" {
					mod = c.Text(src)
					break
				}
			}
			if mod == "" {
				return
			}
			line := int(n.StartPoint().Row) + 1
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: "unresolved::import::" + mod,
				Kind: graph.EdgeImports, FilePath: filePath, Line: line,
			})
		}
	})

	// Call edges: subroutine_call nodes.
	funcRanges := buildFuncRanges(result)
	walkNodes(root, func(n *sitter.Node) {
		if parser.NodeType(n, e.lang) != "subroutine_call" {
			return
		}
		// The first identifier child is the callee name.
		name := ""
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if parser.NodeType(c, e.lang) == "identifier" {
				name = c.Text(src)
				break
			}
		}
		if name == "" || isFortranKeyword(strings.ToLower(name)) {
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

// fortranHeaderName returns the `name` text under the first header
// statement of a given kind inside a module/program/function/subroutine
// node.
func fortranHeaderName(node *sitter.Node, headerType string, lang *sitter.Language, src []byte) string {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		c := node.NamedChild(i)
		if parser.NodeType(c, lang) != headerType {
			continue
		}
		for j := 0; j < int(c.NamedChildCount()); j++ {
			g := c.NamedChild(j)
			if parser.NodeType(g, lang) == "name" {
				return g.Text(src)
			}
		}
	}
	return ""
}

func isFortranKeyword(s string) bool {
	switch s {
	case "if", "then", "else", "elseif", "endif", "end", "do", "while",
		"continue", "cycle", "exit", "return", "goto", "call", "use",
		"implicit", "none", "integer", "real", "double", "complex",
		"logical", "character", "dimension", "allocatable", "pointer",
		"target", "parameter", "save", "intent", "in", "out", "inout",
		"subroutine", "function", "module", "program", "type", "contains",
		"interface", "public", "private", "pure", "elemental", "recursive",
		"where", "forall", "select", "case", "default", "stop":
		return true
	}
	return false
}

var _ parser.Extractor = (*FortranExtractor)(nil)
