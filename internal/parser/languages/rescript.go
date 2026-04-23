package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// ReScript (OCaml-derived) uses `let`, `type`, and `module`. The
// odvcencio grammar emits:
//   - `open_statement` / `include_statement` for imports
//   - `type_declaration` → type_binding → `type_identifier` for types
//   - `module_declaration` → module_binding → `module_identifier` for
//     nested modules
//   - `let_declaration` → `let_binding` (value_identifier = RHS) for
//     both variable bindings and function definitions; a function is
//     distinguished by the RHS being a `function` node.
// Call sites are `call_expression` with an identifier / member_expression
// head.

// ReScriptExtractor extracts ReScript source using tree-sitter.
type ReScriptExtractor struct {
	lang *sitter.Language
}

func NewReScriptExtractor() *ReScriptExtractor {
	return &ReScriptExtractor{lang: grammars.RescriptLanguage()}
}

func (e *ReScriptExtractor) Language() string     { return "rescript" }
func (e *ReScriptExtractor) Extensions() []string { return []string{".res", ".resi"} }

func (e *ReScriptExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	tree, err := parser.ParseFile(src, e.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	root := tree.RootNode()
	lines := strings.Split(string(src), "\n")
	result := &parser.ExtractionResult{}

	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: len(lines),
		Language: "rescript",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	add := func(name string, kind graph.NodeKind, start, end int) {
		if name == "" || isReScriptKeyword(name) {
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
			Language: "rescript",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}
	addImport := func(target string, line int) {
		if target == "" {
			return
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + target,
			Kind: graph.EdgeImports, FilePath: filePath, Line: line,
		})
	}

	walkNodes(root, func(node *sitter.Node) {
		switch parser.NodeType(node, e.lang) {
		case "open_statement", "include_statement":
			mod := e.moduleIdentifier(node, src)
			if mod != "" {
				addImport(mod, int(node.StartPoint().Row)+1)
			}
		case "type_declaration":
			// type_declaration → type_binding → type_identifier
			for i := 0; i < int(node.ChildCount()); i++ {
				c := node.Child(i)
				if c == nil || parser.NodeType(c, e.lang) != "type_binding" {
					continue
				}
				name := e.firstNamedChild(c, "type_identifier", src)
				start := int(node.StartPoint().Row) + 1
				end := findBlockEnd(lines, start)
				add(name, graph.KindType, start, end)
			}
		case "module_declaration":
			for i := 0; i < int(node.ChildCount()); i++ {
				c := node.Child(i)
				if c == nil || parser.NodeType(c, e.lang) != "module_binding" {
					continue
				}
				name := e.firstNamedChild(c, "module_identifier", src)
				start := int(node.StartPoint().Row) + 1
				end := findBlockEnd(lines, start)
				add(name, graph.KindType, start, end)
			}
		case "let_declaration":
			// let_declaration → let_binding → value_identifier [=] RHS
			for i := 0; i < int(node.ChildCount()); i++ {
				binding := node.Child(i)
				if binding == nil || parser.NodeType(binding, e.lang) != "let_binding" {
					continue
				}
				name, rhsType := e.letBindingShape(binding, src)
				if name == "" {
					continue
				}
				start := int(node.StartPoint().Row) + 1
				if rhsType == "function" {
					add(name, graph.KindFunction, start, findBlockEnd(lines, start))
				} else {
					add(name, graph.KindVariable, start, start)
				}
			}
		}
	})

	// Call-site edges inside functions. The grammar emits
	// `call_expression` nodes for function invocations.
	funcRanges := buildFuncRanges(result)
	walkNodes(root, func(node *sitter.Node) {
		if parser.NodeType(node, e.lang) != "call_expression" {
			return
		}
		name := e.callHead(node, src)
		if name == "" || isReScriptKeyword(name) {
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

// moduleIdentifier returns the first module_identifier found inside an
// open/include statement. `include Js.Promise` wraps its pieces in a
// `module_identifier_path`; the first module_identifier inside is the
// top-level name, which matches the regex-era behaviour where the whole
// dotted path was kept verbatim. We honour that here by returning the
// joined text of the path node when present.
func (e *ReScriptExtractor) moduleIdentifier(node *sitter.Node, src []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		switch parser.NodeType(c, e.lang) {
		case "module_identifier":
			return strings.TrimSpace(c.Text(src))
		case "module_identifier_path":
			// First module_identifier inside; the regex captured the
			// leading segment (e.g. `include Js.Promise` → `Js`).
			for j := 0; j < int(c.ChildCount()); j++ {
				inner := c.Child(j)
				if inner == nil {
					continue
				}
				if parser.NodeType(inner, e.lang) == "module_identifier" {
					return strings.TrimSpace(inner.Text(src))
				}
			}
			return strings.TrimSpace(c.Text(src))
		}
	}
	return ""
}

// letBindingShape returns (name, rhsGrammarType). The name is taken
// from the first value_identifier; the RHS type is the grammar type of
// the node appearing after the `=` terminal.
func (e *ReScriptExtractor) letBindingShape(node *sitter.Node, src []byte) (string, string) {
	var name, rhsType string
	sawEq := false
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		t := parser.NodeType(c, e.lang)
		switch {
		case name == "" && t == "value_identifier":
			name = c.Text(src)
		case t == "=":
			sawEq = true
		case sawEq && rhsType == "":
			rhsType = t
		}
	}
	return name, rhsType
}

// firstNamedChild returns the text of the first direct child whose
// grammar type matches.
func (e *ReScriptExtractor) firstNamedChild(node *sitter.Node, typ string, src []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, e.lang) == typ {
			return strings.TrimSpace(c.Text(src))
		}
	}
	return ""
}

// callHead extracts the identifier that introduces a call_expression.
// For `foo()` the head is an `identifier` or `value_identifier`; for
// `Js.Math.sqrt(...)` it is a `member_expression` whose rightmost
// property is what we surface.
func (e *ReScriptExtractor) callHead(node *sitter.Node, src []byte) string {
	if node == nil {
		return ""
	}
	if node.ChildCount() == 0 {
		return ""
	}
	first := node.Child(0)
	if first == nil {
		return ""
	}
	switch parser.NodeType(first, e.lang) {
	case "value_identifier", "identifier":
		return strings.TrimSpace(first.Text(src))
	case "member_expression":
		// last property_identifier child
		for i := int(first.ChildCount()) - 1; i >= 0; i-- {
			c := first.Child(i)
			if c == nil {
				continue
			}
			t := parser.NodeType(c, e.lang)
			if t == "property_identifier" || t == "value_identifier" || t == "identifier" {
				return strings.TrimSpace(c.Text(src))
			}
		}
	}
	return ""
}

func isReScriptKeyword(s string) bool {
	switch s {
	case "if", "else", "for", "while", "switch", "match", "when",
		"return", "break", "continue",
		"let", "rec", "type", "module", "and", "as", "open", "include",
		"external", "mutable", "private", "of", "fun", "try", "catch",
		"true", "false", "lazy", "exception", "assert", "in", "to", "downto":
		return true
	}
	return false
}

var _ parser.Extractor = (*ReScriptExtractor)(nil)
