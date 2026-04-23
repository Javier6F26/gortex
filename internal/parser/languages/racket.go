package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// RacketExtractor extracts Racket source files.
//
// Racket is S-expression based. The odvcencio grammar wraps every
// parenthesised form in a `list` node whose children are the form's
// tokens (`symbol`, nested `list`, `string`, `number`, …). Definitions
// are lists whose first `symbol` is one of:
//   - `define`           — value or function
//   - `define-struct`    — type
//   - `define-syntax`    — macro
//   - `module`           — nested module
// Imports use `(require …)`, with either a string literal path or a
// collection symbol.
type RacketExtractor struct {
	lang *sitter.Language
}

func NewRacketExtractor() *RacketExtractor {
	return &RacketExtractor{lang: grammars.RacketLanguage()}
}

func (e *RacketExtractor) Language() string { return "racket" }
func (e *RacketExtractor) Extensions() []string {
	return []string{".rkt", ".rktl", ".rktd", ".scrbl"}
}

func (e *RacketExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	tree, err := parser.ParseFile(src, e.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	root := tree.RootNode()
	result := &parser.ExtractionResult{}

	endLine := 1
	if root != nil {
		endLine = int(root.EndPoint().Row) + 1
	}
	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: endLine,
		Language: "racket",
	}
	result.Nodes = append(result.Nodes, fileNode)

	if root == nil {
		return result, nil
	}

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
			Language: "racket",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	walkNodes(root, func(n *sitter.Node) {
		if parser.NodeType(n, e.lang) != "list" {
			return
		}
		// First non-punctuation child is the form name (a `symbol`).
		head, headText := firstSymbol(n, src, e.lang)
		if head == nil {
			return
		}
		start := int(n.StartPoint().Row) + 1
		end := int(n.EndPoint().Row) + 1

		switch headText {
		case "define":
			// `(define name value)` or `(define (name args...) body)`.
			target := nextSibling(n, head, e.lang)
			if target == nil {
				return
			}
			switch parser.NodeType(target, e.lang) {
			case "symbol":
				add(target.Text(src), graph.KindVariable, start, start)
			case "list":
				// Function form: first symbol inside is the name.
				if _, name := firstSymbol(target, src, e.lang); name != "" {
					add(name, graph.KindFunction, start, end)
				}
			}

		case "define-struct":
			target := nextSibling(n, head, e.lang)
			if target == nil {
				return
			}
			if parser.NodeType(target, e.lang) == "symbol" {
				add(target.Text(src), graph.KindType, start, start)
			}

		case "define-syntax":
			target := nextSibling(n, head, e.lang)
			if target == nil {
				return
			}
			switch parser.NodeType(target, e.lang) {
			case "symbol":
				add(target.Text(src), graph.KindFunction, start, end)
			case "list":
				if _, name := firstSymbol(target, src, e.lang); name != "" {
					add(name, graph.KindFunction, start, end)
				}
			}

		case "module", "module+", "module*":
			target := nextSibling(n, head, e.lang)
			if target == nil {
				return
			}
			if parser.NodeType(target, e.lang) == "symbol" {
				add(target.Text(src), graph.KindType, start, end)
			}

		case "require":
			// Emit one import edge per sibling after `require`.
			for i := 0; i < int(n.ChildCount()); i++ {
				child := n.Child(i)
				if child == nil || child == head {
					continue
				}
				t := parser.NodeType(child, e.lang)
				var mod string
				switch t {
				case "string":
					mod = strings.Trim(child.Text(src), `"`)
				case "symbol":
					mod = child.Text(src)
				}
				if mod == "" || mod == "require" {
					continue
				}
				result.Edges = append(result.Edges, &graph.Edge{
					From: fileNode.ID, To: "unresolved::import::" + mod,
					Kind: graph.EdgeImports, FilePath: filePath, Line: int(child.StartPoint().Row) + 1,
				})
			}
		}
	})

	return result, nil
}

// firstSymbol returns the first `symbol` child of a `list` node along
// with its text. Returns (nil, "") if none found.
func firstSymbol(list *sitter.Node, src []byte, lang *sitter.Language) (*sitter.Node, string) {
	for i := 0; i < int(list.ChildCount()); i++ {
		child := list.Child(i)
		if child != nil && parser.NodeType(child, lang) == "symbol" {
			return child, child.Text(src)
		}
	}
	return nil, ""
}

// nextSibling returns the child following `after` that has one of the
// interesting types (symbol or list). Returns nil if not found.
func nextSibling(parent, after *sitter.Node, lang *sitter.Language) *sitter.Node {
	passed := false
	for i := 0; i < int(parent.ChildCount()); i++ {
		child := parent.Child(i)
		if child == nil {
			continue
		}
		if !passed {
			if child == after {
				passed = true
			}
			continue
		}
		t := parser.NodeType(child, lang)
		if t == "symbol" || t == "list" {
			return child
		}
	}
	return nil
}

var _ parser.Extractor = (*RacketExtractor)(nil)
