package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// JuliaExtractor extracts Julia source files using tree-sitter.
//
// Relevant odvcencio grammar nodes:
//   module_definition     ("module" identifier block "end")
//   function_definition   ("function" signature block "end")
//   short_function_definition / assignment (when `name(args) = body`
//     the grammar currently emits an `assignment` whose LHS is a
//     call_expression — the old short-function regex matched the same)
//   struct_definition     ("struct" type_head block "end")
//   abstract_definition / primitive_definition (type declarations)
//   macro_definition
//   using_statement / import_statement
//   call_expression       (for `include("…")`, plus general call sites)
type JuliaExtractor struct {
	lang *sitter.Language
}

func NewJuliaExtractor() *JuliaExtractor {
	return &JuliaExtractor{lang: grammars.JuliaLanguage()}
}

func (e *JuliaExtractor) Language() string     { return "julia" }
func (e *JuliaExtractor) Extensions() []string { return []string{".jl"} }

func (e *JuliaExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	tree, err := parser.ParseFile(src, e.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	root := tree.RootNode()
	result := &parser.ExtractionResult{}

	lineCount := strings.Count(string(src), "\n") + 1
	if lineCount < 1 {
		lineCount = 1
	}
	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: lineCount,
		Language: "julia",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	add := func(name string, kind graph.NodeKind, start, end int) {
		if name == "" || isJuliaKeyword(name) {
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
			Language: "julia",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	// Helper: extract a `name` from a call_expression's first identifier.
	callName := func(call *sitter.Node) string {
		if call == nil {
			return ""
		}
		for i := 0; i < int(call.ChildCount()); i++ {
			c := call.Child(i)
			if c == nil {
				continue
			}
			if parser.NodeType(c, e.lang) == "identifier" {
				return c.Text(src)
			}
		}
		return ""
	}

	// Helper: walk a signature / type_head for the first identifier.
	firstIdent := func(n *sitter.Node) string {
		var found string
		var rec func(*sitter.Node)
		rec = func(x *sitter.Node) {
			if x == nil || found != "" {
				return
			}
			if parser.NodeType(x, e.lang) == "identifier" {
				found = x.Text(src)
				return
			}
			for i := 0; i < int(x.ChildCount()); i++ {
				rec(x.Child(i))
			}
		}
		rec(n)
		return found
	}

	walkNodes(root, func(n *sitter.Node) {
		start := int(n.StartPoint().Row) + 1
		end := int(n.EndPoint().Row) + 1

		switch parser.NodeType(n, e.lang) {
		case "module_definition":
			name := firstIdent(n)
			add(name, graph.KindType, start, end)

		case "struct_definition":
			// Name lives under type_head → identifier (may be parameterised).
			name := firstIdent(n)
			add(name, graph.KindType, start, end)

		case "abstract_definition", "primitive_definition":
			name := firstIdent(n)
			add(name, graph.KindType, start, start)

		case "function_definition":
			// Name is the first identifier in the signature child.
			name := firstIdent(n)
			add(name, graph.KindFunction, start, end)

		case "macro_definition":
			name := firstIdent(n)
			add(name, graph.KindFunction, start, end)

		case "assignment":
			// Short-form function: `name(args) = body`. Detect by checking
			// if the LHS is a call_expression.
			if n.ChildCount() == 0 {
				return
			}
			first := n.Child(0)
			if first != nil && parser.NodeType(first, e.lang) == "call_expression" {
				name := callName(first)
				add(name, graph.KindFunction, start, start)
			}

		case "using_statement", "import_statement":
			e.extractImport(n, src, fileNode, filePath, result)

		case "call_expression":
			// `include("file.jl")` is the Julia-flavour import directive.
			if callName(n) == "include" {
				e.extractInclude(n, src, fileNode, filePath, result)
			}
		}
	})

	// Call sites inside functions.
	funcRanges := buildFuncRanges(result)
	walkNodes(root, func(n *sitter.Node) {
		if parser.NodeType(n, e.lang) != "call_expression" {
			return
		}
		name := callName(n)
		if name == "" || isJuliaKeyword(name) || name == "include" {
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

// extractImport handles `using Foo`, `using Foo.Bar`, `import Foo, Bar`.
func (e *JuliaExtractor) extractImport(
	node *sitter.Node, src []byte, fileNode *graph.Node, filePath string,
	result *parser.ExtractionResult,
) {
	line := int(node.StartPoint().Row) + 1
	// Collect dotted identifier paths. The grammar emits named `identifier`
	// children (and possibly `scoped_identifier` for dotted forms).
	var parts []string
	flush := func() {
		if len(parts) == 0 {
			return
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + strings.Join(parts, "."),
			Kind: graph.EdgeImports, FilePath: filePath, Line: line,
		})
		parts = parts[:0]
	}
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		switch parser.NodeType(c, e.lang) {
		case "identifier":
			parts = append(parts, c.Text(src))
		case ",":
			flush()
		case "scoped_identifier":
			// Flatten dotted path.
			var p []string
			for j := 0; j < int(c.ChildCount()); j++ {
				cc := c.Child(j)
				if cc != nil && parser.NodeType(cc, e.lang) == "identifier" {
					p = append(p, cc.Text(src))
				}
			}
			if len(p) > 0 {
				result.Edges = append(result.Edges, &graph.Edge{
					From: fileNode.ID, To: "unresolved::import::" + strings.Join(p, "."),
					Kind: graph.EdgeImports, FilePath: filePath, Line: line,
				})
			}
		}
	}
	flush()
}

// extractInclude handles `include("path.jl")`.
func (e *JuliaExtractor) extractInclude(
	node *sitter.Node, src []byte, fileNode *graph.Node, filePath string,
	result *parser.ExtractionResult,
) {
	line := int(node.StartPoint().Row) + 1
	var args *sitter.Node
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c != nil && parser.NodeType(c, e.lang) == "argument_list" {
			args = c
			break
		}
	}
	if args == nil {
		return
	}
	// Pick first string_literal → content child.
	for i := 0; i < int(args.ChildCount()); i++ {
		c := args.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, e.lang) != "string_literal" {
			continue
		}
		for j := 0; j < int(c.ChildCount()); j++ {
			cc := c.Child(j)
			if cc == nil {
				continue
			}
			if parser.NodeType(cc, e.lang) == "content" {
				result.Edges = append(result.Edges, &graph.Edge{
					From: fileNode.ID, To: "unresolved::import::" + cc.Text(src),
					Kind: graph.EdgeImports, FilePath: filePath, Line: line,
				})
				return
			}
		}
		// Fallback: strip quotes from the raw literal text.
		raw := strings.Trim(c.Text(src), `"'`)
		if raw != "" {
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: "unresolved::import::" + raw,
				Kind: graph.EdgeImports, FilePath: filePath, Line: line,
			})
			return
		}
	}
}

func isJuliaKeyword(s string) bool {
	switch s {
	case "if", "else", "elseif", "end", "for", "while", "do", "break", "continue",
		"return", "function", "macro", "module", "baremodule", "struct", "mutable",
		"abstract", "primitive", "type", "import", "using", "export", "let",
		"local", "global", "const", "begin", "try", "catch", "finally", "throw",
		"where", "in", "isa", "true", "false", "nothing", "missing":
		return true
	}
	return false
}

var _ parser.Extractor = (*JuliaExtractor)(nil)
