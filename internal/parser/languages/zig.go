package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// ZigExtractor extracts Zig source files using tree-sitter.
type ZigExtractor struct {
	lang *sitter.Language
}

func NewZigExtractor() *ZigExtractor {
	return &ZigExtractor{lang: grammars.ZigLanguage()}
}

func (e *ZigExtractor) Language() string     { return "zig" }
func (e *ZigExtractor) Extensions() []string { return []string{".zig"} }

func (e *ZigExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "zig",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)

	// Walk the AST to extract declarations and variable bindings.
	walkNodes(root, func(n *sitter.Node) {
		if n == nil {
			return
		}
		switch parser.NodeType(n, e.lang) {
		case "function_declaration":
			e.extractFunction(n, src, filePath, fileNode, result, seen)
		case "variable_declaration":
			e.extractVariable(n, src, filePath, fileNode, result, seen)
		}
	})

	// Imports: @import("…") builtin_function calls.
	e.extractImports(root, src, filePath, fileNode, result)

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
		if n.ChildCount() == 0 {
			return
		}
		target := n.Child(0)
		switch parser.NodeType(target, e.lang) {
		case "identifier":
			name := target.Text(src)
			if isZigKeyword(name) {
				return
			}
			result.Edges = append(result.Edges, &graph.Edge{
				From: callerID, To: "unresolved::" + name,
				Kind: graph.EdgeCalls, FilePath: filePath, Line: line,
			})
		case "field_expression", "selector_expression":
			// obj.method(...) — record the trailing field as *.method.
			if methodName := zigLastFieldName(target, src, e.lang); methodName != "" {
				result.Edges = append(result.Edges, &graph.Edge{
					From: callerID, To: "unresolved::*." + methodName,
					Kind: graph.EdgeCalls, FilePath: filePath, Line: line,
				})
			}
		}
	})

	return result, nil
}

func (e *ZigExtractor) extractFunction(
	node *sitter.Node, src []byte, filePath string, fileNode *graph.Node,
	result *parser.ExtractionResult, seen map[string]bool,
) {
	name := firstDirectIdentifier(node, src, e.lang)
	if name == "" {
		return
	}
	id := filePath + "::" + name
	if seen[id] {
		return
	}
	seen[id] = true

	startLine := int(node.StartPoint().Row) + 1
	endLine := int(node.EndPoint().Row) + 1
	result.Nodes = append(result.Nodes, &graph.Node{
		ID: id, Kind: graph.KindFunction, Name: name,
		FilePath: filePath, StartLine: startLine, EndLine: endLine,
		Language: "zig",
	})
	result.Edges = append(result.Edges, &graph.Edge{
		From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
		FilePath: filePath, Line: startLine,
	})
}

// extractVariable handles top-level const/var declarations. When the RHS
// is a struct/enum/union declaration, we emit a KindType node and record
// the type_kind in meta. Otherwise we emit a KindVariable node.
func (e *ZigExtractor) extractVariable(
	node *sitter.Node, src []byte, filePath string, fileNode *graph.Node,
	result *parser.ExtractionResult, seen map[string]bool,
) {
	name := firstDirectIdentifier(node, src, e.lang)
	if name == "" {
		return
	}
	id := filePath + "::" + name
	if seen[id] {
		return
	}

	startLine := int(node.StartPoint().Row) + 1

	// Detect type-ish RHS: struct/enum/union declaration among children.
	typeKind := ""
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		switch parser.NodeType(c, e.lang) {
		case "struct_declaration":
			typeKind = "struct"
		case "enum_declaration":
			typeKind = "enum"
		case "union_declaration":
			typeKind = "union"
		}
		if typeKind != "" {
			break
		}
	}

	if typeKind != "" {
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindType, Name: name,
			FilePath: filePath, StartLine: startLine, EndLine: startLine,
			Language: "zig",
			Meta:     map[string]any{"type_kind": typeKind},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: startLine,
		})
		return
	}

	seen[id] = true
	result.Nodes = append(result.Nodes, &graph.Node{
		ID: id, Kind: graph.KindVariable, Name: name,
		FilePath: filePath, StartLine: startLine, EndLine: startLine,
		Language: "zig",
	})
	result.Edges = append(result.Edges, &graph.Edge{
		From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
		FilePath: filePath, Line: startLine,
	})
}

// extractImports records each @import("module") as an import edge.
func (e *ZigExtractor) extractImports(
	root *sitter.Node, src []byte, filePath string, fileNode *graph.Node,
	result *parser.ExtractionResult,
) {
	walkNodes(root, func(n *sitter.Node) {
		if parser.NodeType(n, e.lang) != "builtin_function" {
			return
		}
		// Expect: builtin_identifier "@import"  arguments (string …)
		var isImport bool
		var mod string
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			if c == nil {
				continue
			}
			switch parser.NodeType(c, e.lang) {
			case "builtin_identifier":
				if c.Text(src) == "@import" {
					isImport = true
				}
			case "arguments":
				for j := 0; j < int(c.ChildCount()); j++ {
					a := c.Child(j)
					if a == nil {
						continue
					}
					if parser.NodeType(a, e.lang) == "string" {
						mod = strings.Trim(a.Text(src), `"`)
						break
					}
				}
			}
		}
		if !isImport || mod == "" {
			return
		}
		line := int(n.StartPoint().Row) + 1
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + mod,
			Kind: graph.EdgeImports, FilePath: filePath, Line: line,
		})
	})
}

// firstDirectIdentifier returns the text of the first direct "identifier"
// child of node.
func firstDirectIdentifier(node *sitter.Node, src []byte, lang *sitter.Language) string {
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

// zigLastFieldName walks a field_expression / selector_expression and
// returns the final field identifier (the method being called).
func zigLastFieldName(node *sitter.Node, src []byte, lang *sitter.Language) string {
	name := ""
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		switch parser.NodeType(c, lang) {
		case "identifier", "field_identifier":
			name = c.Text(src)
		case "field_expression", "selector_expression":
			if inner := zigLastFieldName(c, src, lang); inner != "" {
				name = inner
			}
		}
	}
	return name
}

func isZigKeyword(s string) bool {
	switch s {
	case "fn", "pub", "const", "var", "if", "else", "while", "for",
		"switch", "return", "break", "continue", "defer", "errdefer",
		"try", "catch", "orelse", "and", "or", "not",
		"struct", "enum", "union", "error", "comptime", "inline",
		"test", "true", "false", "null", "undefined", "unreachable",
		"usingnamespace":
		return true
	}
	return false
}

// lineAt returns the 1-based line number for byte offset pos.
func lineAt(src []byte, pos int) int {
	line := 1
	for i := 0; i < pos && i < len(src); i++ {
		if src[i] == '\n' {
			line++
		}
	}
	return line
}

// findBlockEnd finds the approximate end line of a brace-delimited block
// starting at startLine (1-based). Kept here because other adapters still
// depend on it.
func findBlockEnd(lines []string, startLine int) int {
	depth := 0
	for i := startLine - 1; i < len(lines); i++ {
		for _, ch := range lines[i] {
			switch ch {
			case '{':
				depth++
			case '}':
				depth--
				if depth <= 0 {
					return i + 1
				}
			}
		}
	}
	return startLine
}

var _ parser.Extractor = (*ZigExtractor)(nil)
