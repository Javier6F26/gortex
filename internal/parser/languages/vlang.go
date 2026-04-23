package languages

import (
	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// VlangExtractor extracts V source using tree-sitter.
type VlangExtractor struct {
	lang *sitter.Language
}

func NewVlangExtractor() *VlangExtractor {
	return &VlangExtractor{lang: grammars.VLanguage()}
}

func (e *VlangExtractor) Language() string     { return "v" }
func (e *VlangExtractor) Extensions() []string { return []string{".v", ".vsh"} }

func (e *VlangExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "v",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)

	add := func(name string, kind graph.NodeKind, startLine, endLine int) {
		if name == "" || isVlangKeyword(name) {
			return
		}
		id := filePath + "::" + name
		if seen[id] {
			return
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: kind, Name: name,
			FilePath: filePath, StartLine: startLine, EndLine: endLine,
			Language: "v",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: startLine,
		})
	}

	// Walk the tree and extract declarations at any depth (top-level is
	// fine for V, but this is more robust).
	walkNodes(root, func(n *sitter.Node) {
		if n == nil {
			return
		}
		startLine := int(n.StartPoint().Row) + 1
		endLine := int(n.EndPoint().Row) + 1

		switch parser.NodeType(n, e.lang) {
		case "module_clause":
			// module geo → type-kind node named "geo" (keeps parity with
			// the regex implementation that tracked the module name).
			if name := findChildIdentifier(n, src, e.lang); name != "" {
				add(name, graph.KindType, startLine, startLine)
			}

		case "function_declaration":
			if name := findChildIdentifier(n, src, e.lang); name != "" {
				add(name, graph.KindFunction, startLine, endLine)
			}

		case "struct_declaration":
			if name := findChildIdentifier(n, src, e.lang); name != "" {
				add(name, graph.KindType, startLine, endLine)
			}

		case "interface_declaration":
			if name := findChildIdentifier(n, src, e.lang); name != "" {
				add(name, graph.KindType, startLine, endLine)
			}

		case "enum_declaration":
			if name := findChildIdentifier(n, src, e.lang); name != "" {
				add(name, graph.KindType, startLine, endLine)
			}

		case "type_declaration":
			// type MyInt = int — single-line alias, keep start=end.
			if name := findChildIdentifier(n, src, e.lang); name != "" {
				add(name, graph.KindType, startLine, startLine)
			}

		case "import_declaration":
			// import foo   /   import foo.bar   /   import foo as f
			if mod := findImportPath(n, src, e.lang); mod != "" {
				result.Edges = append(result.Edges, &graph.Edge{
					From: fileNode.ID, To: "unresolved::import::" + mod,
					Kind: graph.EdgeImports, FilePath: filePath, Line: startLine,
				})
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

		// The call target is the first child (identifier, selector_expression, …).
		if n.ChildCount() == 0 {
			return
		}
		target := n.Child(0)
		switch parser.NodeType(target, e.lang) {
		case "reference_expression", "identifier":
			name := vlangRefName(target, src, e.lang)
			if name == "" || isVlangKeyword(name) {
				return
			}
			if callerID != "" && idSuffix(callerID) == name {
				return
			}
			result.Edges = append(result.Edges, &graph.Edge{
				From: callerID, To: "unresolved::" + name,
				Kind: graph.EdgeCalls, FilePath: filePath, Line: line,
			})
		case "selector_expression":
			// obj.method(...) — record the trailing identifier as *.method.
			methodName := lastSelectorIdentifier(target, src, e.lang)
			if methodName == "" || isVlangKeyword(methodName) {
				return
			}
			result.Edges = append(result.Edges, &graph.Edge{
				From: callerID, To: "unresolved::*." + methodName,
				Kind: graph.EdgeCalls, FilePath: filePath, Line: line,
			})
		}
	})

	return result, nil
}

// findChildIdentifier returns the text of the first direct "identifier"
// child of node (the declaration name for V's *_declaration nodes).
func findChildIdentifier(node *sitter.Node, src []byte, lang *sitter.Language) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, lang) == "identifier" {
			return c.Text(src)
		}
	}
	return ""
}

// findImportPath walks into an import_declaration and returns the dotted
// import path text (e.g. "math", "json", "foo.bar").
func findImportPath(node *sitter.Node, src []byte, lang *sitter.Language) string {
	var path string
	walkNodes(node, func(n *sitter.Node) {
		if path != "" {
			return
		}
		if parser.NodeType(n, lang) == "import_path" {
			path = n.Text(src)
		}
	})
	return path
}

// vlangRefName returns the identifier text for a reference_expression or
// bare identifier node.
func vlangRefName(node *sitter.Node, src []byte, lang *sitter.Language) string {
	if parser.NodeType(node, lang) == "identifier" {
		return node.Text(src)
	}
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, lang) == "identifier" {
			return c.Text(src)
		}
	}
	return ""
}

// lastSelectorIdentifier walks a selector_expression (a.b.c) and returns
// the trailing identifier name (c).
func lastSelectorIdentifier(node *sitter.Node, src []byte, lang *sitter.Language) string {
	name := ""
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		switch parser.NodeType(c, lang) {
		case "reference_expression":
			if id := vlangRefName(c, src, lang); id != "" {
				name = id
			}
		case "identifier":
			name = c.Text(src)
		case "selector_expression":
			if id := lastSelectorIdentifier(c, src, lang); id != "" {
				name = id
			}
		}
	}
	return name
}

// idSuffix returns the symbol name portion of an ID like "path::name".
func idSuffix(id string) string {
	for i := len(id) - 1; i >= 1; i-- {
		if id[i-1] == ':' && id[i] == ':' {
			// Actually the separator is "::" so step back one more.
			return id[i+1:]
		}
	}
	// Fallback: find last "::".
	for i := len(id) - 2; i >= 0; i-- {
		if id[i] == ':' && id[i+1] == ':' {
			return id[i+2:]
		}
	}
	return id
}

func isVlangKeyword(s string) bool {
	switch s {
	case "if", "else", "for", "match", "in", "is", "or", "and",
		"return", "defer", "go", "spawn", "break", "continue",
		"fn", "struct", "interface", "enum", "type", "const",
		"module", "import", "pub", "mut", "shared", "static",
		"true", "false", "none", "as", "unsafe", "asm", "lock", "rlock":
		return true
	}
	return false
}

var _ parser.Extractor = (*VlangExtractor)(nil)
