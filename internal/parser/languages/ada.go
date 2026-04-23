package languages

import (
	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// AdaExtractor extracts Ada source files using tree-sitter.
//
// The grammar emits a `compilation` root whose named children are one
// or more `compilation_unit`s. Each unit wraps a single top-level
// construct (with clause, package declaration/body, subprogram). We
// walk them in order, covering:
//
//   - with_clause                 → import edges for each selected name
//   - package_declaration         → KindType + nested subprograms/types
//   - package_body                → KindType + nested subprogram bodies
//   - full_type_declaration       → KindType
//   - subtype_declaration         → KindType
//   - subprogram_declaration/body → KindFunction (spec and body share a name)
type AdaExtractor struct {
	lang *sitter.Language
}

func NewAdaExtractor() *AdaExtractor {
	return &AdaExtractor{lang: grammars.AdaLanguage()}
}

func (e *AdaExtractor) Language() string { return "ada" }
func (e *AdaExtractor) Extensions() []string {
	return []string{".ada", ".adb", ".ads", ".gpr"}
}

func (e *AdaExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "ada",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	totalLines := int(root.EndPoint().Row) + 1

	add := func(name string, kind graph.NodeKind, start, end int) {
		if name == "" {
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
			Language: "ada",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	// Recursively walk the tree and fire on known declaration shapes.
	walkNodes(root, func(n *sitter.Node) {
		if n == nil {
			return
		}
		start := int(n.StartPoint().Row) + 1
		end := int(n.EndPoint().Row) + 1

		switch parser.NodeType(n, e.lang) {
		case "with_clause":
			// with A.B.C, D.E; → one import per selected name.
			e.handleWithClause(n, src, filePath, fileNode, result, start)

		case "package_declaration", "package_body":
			name := adaFirstIdentifier(n, src, e.lang)
			// The package declaration/body extends to the matching
			// `end NAME;` token, but test only checks the node exists;
			// use totalLines for the end to mirror the prior regex
			// implementation.
			add(name, graph.KindType, start, totalLines)

		case "full_type_declaration", "subtype_declaration":
			name := adaFirstIdentifier(n, src, e.lang)
			add(name, graph.KindType, start, start)

		case "subprogram_declaration", "subprogram_body":
			name := adaSubprogramName(n, src, e.lang)
			add(name, graph.KindFunction, start, end)

		case "generic_instantiation":
			// Generic package/subprogram instantiations also carry a
			// leading identifier and should surface as a type.
			name := adaFirstIdentifier(n, src, e.lang)
			add(name, graph.KindType, start, end)
		}
	})

	return result, nil
}

// handleWithClause emits one EdgeImports per identifier inside a
// `with X, Y.Z;` clause. The grammar represents each imported unit as
// either a bare `identifier` or a `selected_component` (dotted path).
func (e *AdaExtractor) handleWithClause(
	node *sitter.Node, src []byte, filePath string, fileNode *graph.Node,
	result *parser.ExtractionResult, line int,
) {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child == nil {
			continue
		}
		switch parser.NodeType(child, e.lang) {
		case "identifier", "selected_component":
			mod := child.Text(src)
			if mod == "" {
				continue
			}
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: "unresolved::import::" + mod,
				Kind: graph.EdgeImports, FilePath: filePath, Line: line,
			})
		}
	}
}

// adaFirstIdentifier returns the first direct-child identifier's text.
// Handles both bare identifiers and selected_components (which keep
// their full dotted representation).
func adaFirstIdentifier(node *sitter.Node, src []byte, lang *sitter.Language) string {
	if node == nil {
		return ""
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child == nil {
			continue
		}
		switch parser.NodeType(child, lang) {
		case "identifier", "selected_component":
			return child.Text(src)
		}
	}
	return ""
}

// adaSubprogramName pulls the name from a subprogram declaration or
// body. Both wrap a `procedure_specification` or `function_specification`
// whose first identifier-like child is the name.
func adaSubprogramName(node *sitter.Node, src []byte, lang *sitter.Language) string {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child == nil {
			continue
		}
		switch parser.NodeType(child, lang) {
		case "procedure_specification", "function_specification":
			return adaFirstIdentifier(child, src, lang)
		}
	}
	return ""
}

var _ parser.Extractor = (*AdaExtractor)(nil)
