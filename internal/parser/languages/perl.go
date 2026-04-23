package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// PerlExtractor extracts Perl source files.
//
// The odvcencio grammar emits:
//   - `package_statement` — `package` keyword followed by a `package`
//     (name) node.
//   - `use_statement` / `require_statement` — `use|require` keyword
//     followed by a `package` (name) node.
//   - `subroutine_declaration_statement` — `sub` + `bareword` (name)
//     + `block`.
//   - `ambiguous_function_call_expression` — `function` (name) + args.
type PerlExtractor struct {
	lang *sitter.Language
}

func NewPerlExtractor() *PerlExtractor {
	return &PerlExtractor{lang: grammars.PerlLanguage()}
}

func (e *PerlExtractor) Language() string     { return "perl" }
func (e *PerlExtractor) Extensions() []string { return []string{".pl", ".pm", ".t", ".pod"} }

func (e *PerlExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "perl",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	add := func(name string, kind graph.NodeKind, start, end int) {
		if name == "" || isPerlKeyword(name) {
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
			Language: "perl",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	// First pass: top-level declarations and imports.
	walkNodes(root, func(n *sitter.Node) {
		switch parser.NodeType(n, e.lang) {
		case "package_statement":
			name := extractPerlPackageName(n, src, e.lang)
			line := int(n.StartPoint().Row) + 1
			add(name, graph.KindType, line, int(n.EndPoint().Row)+1)

		case "use_statement", "require_statement", "no_statement":
			mod := extractPerlPackageName(n, src, e.lang)
			if mod == "" {
				return
			}
			line := int(n.StartPoint().Row) + 1
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: "unresolved::import::" + mod,
				Kind: graph.EdgeImports, FilePath: filePath, Line: line,
			})

		case "subroutine_declaration_statement":
			name := firstChildOfTypeText(n, "bareword", src, e.lang)
			line := int(n.StartPoint().Row) + 1
			end := int(n.EndPoint().Row) + 1
			add(name, graph.KindFunction, line, end)
		}
	})

	// Second pass: call edges.
	funcRanges := buildFuncRanges(result)
	walkNodes(root, func(n *sitter.Node) {
		if parser.NodeType(n, e.lang) != "ambiguous_function_call_expression" {
			return
		}
		// Child 0 is the function name (node type `function`).
		var name string
		for i := 0; i < int(n.ChildCount()); i++ {
			child := n.Child(i)
			if child != nil && parser.NodeType(child, e.lang) == "function" {
				name = strings.TrimSpace(child.Text(src))
				break
			}
		}
		if name == "" || isPerlKeyword(name) {
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

// extractPerlPackageName returns the package/module name from a
// `package_statement` / `use_statement` / `require_statement`. The
// grammar emits the keyword and the name both as nodes of type
// `package` — we want the second one, which is the name.
func extractPerlPackageName(node *sitter.Node, src []byte, lang *sitter.Language) string {
	var candidates []string
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		if parser.NodeType(child, lang) == "package" {
			candidates = append(candidates, child.Text(src))
		}
	}
	for _, c := range candidates {
		// Skip the keyword (`package`) — name is the other occurrence.
		if c == "package" {
			continue
		}
		return c
	}
	return ""
}

func isPerlKeyword(s string) bool {
	switch s {
	case "if", "elsif", "else", "unless", "while", "until", "for", "foreach",
		"do", "last", "next", "redo", "return", "my", "our", "local", "state",
		"sub", "package", "use", "no", "require", "defined", "undef", "and",
		"or", "not", "xor", "eq", "ne", "lt", "gt", "le", "ge", "cmp":
		return true
	}
	return false
}

var _ parser.Extractor = (*PerlExtractor)(nil)
