package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// GDScriptExtractor extracts Godot GDScript using tree-sitter.
type GDScriptExtractor struct {
	lang *sitter.Language
}

func NewGDScriptExtractor() *GDScriptExtractor {
	return &GDScriptExtractor{lang: grammars.GdscriptLanguage()}
}

func (e *GDScriptExtractor) Language() string     { return "gdscript" }
func (e *GDScriptExtractor) Extensions() []string { return []string{".gd"} }

func (e *GDScriptExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "gdscript",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	add := func(name string, kind graph.NodeKind, start, end int, meta map[string]any) {
		if name == "" || isGDKeyword(name) {
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
			Language: "gdscript", Meta: meta,
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	// Extract top-level constructs.
	walkNodes(root, func(n *sitter.Node) {
		t := parser.NodeType(n, e.lang)
		switch t {
		case "class_name_statement":
			name := firstNamedChildTextByType(n, "name", e.lang, src)
			add(name, graph.KindType,
				int(n.StartPoint().Row)+1, int(n.StartPoint().Row)+1,
				map[string]any{"gd_kind": "class_name"})

		case "class_definition":
			name := firstNamedChildTextByType(n, "name", e.lang, src)
			add(name, graph.KindType,
				int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1,
				map[string]any{"gd_kind": "inner_class"})

		case "function_definition":
			name := firstNamedChildTextByType(n, "name", e.lang, src)
			add(name, graph.KindFunction,
				int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, nil)

		case "variable_statement":
			name := firstNamedChildTextByType(n, "name", e.lang, src)
			add(name, graph.KindVariable,
				int(n.StartPoint().Row)+1, int(n.StartPoint().Row)+1, nil)

		case "const_statement":
			name := firstNamedChildTextByType(n, "name", e.lang, src)
			add(name, graph.KindVariable,
				int(n.StartPoint().Row)+1, int(n.StartPoint().Row)+1,
				map[string]any{"const": true})

		case "enum_definition":
			name := firstNamedChildTextByType(n, "name", e.lang, src)
			add(name, graph.KindType,
				int(n.StartPoint().Row)+1, int(n.StartPoint().Row)+1,
				map[string]any{"gd_kind": "enum"})

		case "signal_statement":
			name := firstNamedChildTextByType(n, "name", e.lang, src)
			add(name, graph.KindMethod,
				int(n.StartPoint().Row)+1, int(n.StartPoint().Row)+1,
				map[string]any{"gd_kind": "signal"})

		case "extends_statement":
			// extends_statement → type → identifier ("." separated in
			// the grammar's `type` node when qualified).
			parent := ""
			for i := 0; i < int(n.NamedChildCount()); i++ {
				c := n.NamedChild(i)
				if parser.NodeType(c, e.lang) == "type" {
					parent = c.Text(src)
					break
				}
			}
			if parent == "" {
				return
			}
			line := int(n.StartPoint().Row) + 1
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: "unresolved::" + parent,
				Kind: graph.EdgeImports, FilePath: filePath, Line: line,
			})
		}
	})

	// preload(...) / load(...) imports.
	walkNodes(root, func(n *sitter.Node) {
		if parser.NodeType(n, e.lang) != "call" {
			return
		}
		var funcName string
		var firstStringArg string
	loop:
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			switch parser.NodeType(c, e.lang) {
			case "identifier":
				if funcName == "" {
					funcName = c.Text(src)
				}
			case "arguments":
				for j := 0; j < int(c.NamedChildCount()); j++ {
					arg := c.NamedChild(j)
					if parser.NodeType(arg, e.lang) == "string" {
						firstStringArg = strings.Trim(arg.Text(src), `"'`)
						break loop
					}
				}
			}
		}
		if funcName != "preload" && funcName != "load" {
			return
		}
		if firstStringArg == "" {
			return
		}
		line := int(n.StartPoint().Row) + 1
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + firstStringArg,
			Kind: graph.EdgeImports, FilePath: filePath, Line: line,
		})
	})

	// Call edges.
	funcRanges := buildFuncRanges(result)
	walkNodes(root, func(n *sitter.Node) {
		if parser.NodeType(n, e.lang) != "call" {
			return
		}
		name := ""
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if parser.NodeType(c, e.lang) == "identifier" {
				name = c.Text(src)
				break
			}
		}
		if name == "" || isGDKeyword(name) || name == "preload" || name == "load" {
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

// firstNamedChildTextByType returns the text of the first named child
// whose type matches.
func firstNamedChildTextByType(node *sitter.Node, typ string, lang *sitter.Language, src []byte) string {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		c := node.NamedChild(i)
		if parser.NodeType(c, lang) == typ {
			return c.Text(src)
		}
	}
	return ""
}

func isGDKeyword(s string) bool {
	switch s {
	case "if", "elif", "else", "for", "while", "match", "break",
		"continue", "pass", "return", "func", "var", "const", "enum",
		"class", "class_name", "extends", "signal", "static", "export",
		"onready", "tool", "self", "null", "true", "false", "and",
		"or", "not", "in", "is", "as", "void", "int", "float", "bool",
		"String", "Vector2", "Vector3", "Array", "Dictionary",
		"preload", "load":
		return true
	}
	return false
}

var _ parser.Extractor = (*GDScriptExtractor)(nil)
