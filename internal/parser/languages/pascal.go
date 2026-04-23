package languages

import (
	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// PascalExtractor extracts Pascal / Delphi source files.
//
// Pascal is case-insensitive. The odvcencio grammar emits:
//   - `unit` / `program` / `package` as top-level wrappers with a
//     `moduleName` child.
//   - `declUses` with one `moduleName` per imported unit.
//   - `declTypes` → `declType` (identifier + declClass|declRecord|
//     declInterface|declObject body).
//   - `defProc` (implementation) and `declProc` (declarations).
//     The name may be bare (`identifier`) or class-qualified
//     (`genericDot` containing Owner.Method).
type PascalExtractor struct {
	lang *sitter.Language
}

func NewPascalExtractor() *PascalExtractor {
	return &PascalExtractor{lang: grammars.PascalLanguage()}
}

func (e *PascalExtractor) Language() string { return "pascal" }
func (e *PascalExtractor) Extensions() []string {
	return []string{".pas", ".pp", ".dpr", ".dpk", ".inc", ".lpr", ".lfm"}
}

func (e *PascalExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "pascal",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
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
			Language: "pascal",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	// Walk the entire AST since declarations can nest under `interface`,
	// `implementation`, `unit`, `program`, etc.
	walkNodes(root, func(n *sitter.Node) {
		switch parser.NodeType(n, e.lang) {
		case "unit", "program", "package":
			name := extractPascalModuleName(n, src, e.lang)
			line := int(n.StartPoint().Row) + 1
			kind := graph.KindType
			if parser.NodeType(n, e.lang) == "program" {
				kind = graph.KindFunction
			}
			end := int(n.EndPoint().Row) + 1
			add(name, kind, line, end)

		case "declUses":
			line := int(n.StartPoint().Row) + 1
			for i := 0; i < int(n.ChildCount()); i++ {
				child := n.Child(i)
				if child == nil || parser.NodeType(child, e.lang) != "moduleName" {
					continue
				}
				mod := child.Text(src)
				if mod == "" {
					continue
				}
				result.Edges = append(result.Edges, &graph.Edge{
					From: fileNode.ID, To: "unresolved::import::" + mod,
					Kind: graph.EdgeImports, FilePath: filePath, Line: line,
				})
			}

		case "declType":
			// `Name = class | record | interface | object`
			name := firstChildOfTypeText(n, "identifier", src, e.lang)
			if name == "" {
				return
			}
			line := int(n.StartPoint().Row) + 1
			end := int(n.EndPoint().Row) + 1
			kind := graph.KindType
			for i := 0; i < int(n.ChildCount()); i++ {
				child := n.Child(i)
				if child != nil && parser.NodeType(child, e.lang) == "declInterface" {
					kind = graph.KindInterface
					break
				}
			}
			add(name, kind, line, end)

		case "defProc":
			// `procedure|function|constructor|destructor Owner.Name(...)`
			// with a body. The name lives on the inner `declProc`.
			name := extractPascalProcName(n, src, e.lang)
			if name == "" {
				return
			}
			line := int(n.StartPoint().Row) + 1
			end := int(n.EndPoint().Row) + 1
			add(name, graph.KindMethod, line, end)
		}
	})

	return result, nil
}

// extractPascalModuleName returns the text of the first moduleName child.
func extractPascalModuleName(node *sitter.Node, src []byte, lang *sitter.Language) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child != nil && parser.NodeType(child, lang) == "moduleName" {
			return child.Text(src)
		}
	}
	return ""
}

// extractPascalProcName returns the name of a procedure/function/constructor/destructor
// from a `defProc` or `declProc`. It understands both bare `identifier` and
// class-qualified `genericDot` (Owner.Name) forms. For `defProc` it descends
// into the inner `declProc` first.
func extractPascalProcName(node *sitter.Node, src []byte, lang *sitter.Language) string {
	// If this is defProc, find the child declProc.
	target := node
	if parser.NodeType(node, lang) == "defProc" {
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			if child != nil && parser.NodeType(child, lang) == "declProc" {
				target = child
				break
			}
		}
	}
	// Look for genericDot or identifier directly under declProc.
	for i := 0; i < int(target.ChildCount()); i++ {
		child := target.Child(i)
		if child == nil {
			continue
		}
		switch parser.NodeType(child, lang) {
		case "genericDot":
			// Collect identifiers joined by kDot.
			var parts []string
			for j := 0; j < int(child.ChildCount()); j++ {
				inner := child.Child(j)
				if inner != nil && parser.NodeType(inner, lang) == "identifier" {
					parts = append(parts, inner.Text(src))
				}
			}
			if len(parts) > 0 {
				return joinWithDot(parts)
			}
		case "identifier":
			return child.Text(src)
		}
	}
	return ""
}

func joinWithDot(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += "." + p
	}
	return out
}

var _ parser.Extractor = (*PascalExtractor)(nil)
