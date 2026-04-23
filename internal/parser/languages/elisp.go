package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// Emacs Lisp. Definitions are S-expressions; we grab the common `def*`
// forms and module-level `require` / `load` / `provide`. Call sites
// are any `(name ...)` inside a `defun` body; the extractor filters
// against a keyword list to reduce noise.
//
// The odvcencio Elisp grammar exposes:
//   source_file
//     function_definition → "(" defun symbol (params) body...  ")"
//     special_form        → "(" defvar|defconst|defcustom|defface|defgroup symbol ... ")"
//     list                → "(" symbol ...args ")"
//         - require's arg is (quote (' symbol))
//         - load's arg is (string "...")
// Generic calls fall under `list` with a leading `symbol`.

// EmacsLispExtractor extracts Emacs Lisp source files into graph nodes and edges.
type EmacsLispExtractor struct {
	lang *sitter.Language
}

func NewEmacsLispExtractor() *EmacsLispExtractor {
	return &EmacsLispExtractor{lang: grammars.ElispLanguage()}
}

func (e *EmacsLispExtractor) Language() string { return "elisp" }
func (e *EmacsLispExtractor) Extensions() []string {
	return []string{".el", ".elc"}
}

func (e *EmacsLispExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "elisp",
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
			Language: "elisp",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	// firstSymbolAfter returns the first `symbol` child whose start is at or
	// after the node named by `afterTypes` within the parent.
	firstSymbol := func(node *sitter.Node) string {
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			if child != nil && parser.NodeType(child, e.lang) == "symbol" {
				return child.Text(src)
			}
		}
		return ""
	}

	// Walk the tree for defs and top-level require/load lists.
	walkNodes(root, func(node *sitter.Node) {
		t := parser.NodeType(node, e.lang)
		startLine := int(node.StartPoint().Row) + 1
		endLine := int(node.EndPoint().Row) + 1

		switch t {
		case "function_definition":
			// ( defun SYMBOL (args) body... )
			add(firstSymbol(node), graph.KindFunction, startLine, endLine)

		case "special_form":
			// (defvar NAME ...), (defconst NAME ...), …
			// The form head is a keyword child (defvar/defconst/…),
			// followed by the symbol we want.
			head := ""
			for i := 0; i < int(node.ChildCount()); i++ {
				child := node.Child(i)
				if child == nil {
					continue
				}
				ct := parser.NodeType(child, e.lang)
				if head == "" && isElispDefKeyword(ct) {
					head = ct
					continue
				}
				if head != "" && ct == "symbol" {
					add(child.Text(src), graph.KindVariable, startLine, endLine)
					break
				}
			}

		case "list":
			// Could be (require 'foo) or (load "bar") — handle imports.
			e.handleListForm(node, src, fileNode.ID, filePath, result)
		}
	})

	// Call sites — any `list` with a leading symbol inside a defun body.
	funcRanges := buildFuncRanges(result)
	walkNodes(root, func(node *sitter.Node) {
		if parser.NodeType(node, e.lang) != "list" {
			return
		}
		// Find the leading symbol.
		var headSym *sitter.Node
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			if child == nil {
				continue
			}
			if parser.NodeType(child, e.lang) == "symbol" {
				headSym = child
				break
			}
		}
		if headSym == nil {
			return
		}
		name := headSym.Text(src)
		if isElispKeyword(name) {
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

// handleListForm inspects a top-level `list` node for require/load forms
// and emits import edges.
func (e *EmacsLispExtractor) handleListForm(
	node *sitter.Node, src []byte, fileID, filePath string,
	result *parser.ExtractionResult,
) {
	var head string
	var headLine int
	var argString, argQuoteSym string

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		ct := parser.NodeType(child, e.lang)
		switch ct {
		case "symbol":
			if head == "" {
				head = child.Text(src)
				headLine = int(child.StartPoint().Row) + 1
			}
		case "string":
			if argString == "" {
				argString = strings.Trim(child.Text(src), `"`)
			}
		case "quote":
			// (quote (' symbol))
			for j := 0; j < int(child.ChildCount()); j++ {
				gc := child.Child(j)
				if gc != nil && parser.NodeType(gc, e.lang) == "symbol" {
					argQuoteSym = gc.Text(src)
					break
				}
			}
		}
	}
	if head == "" {
		return
	}

	var mod string
	switch head {
	case "require":
		mod = argQuoteSym
	case "load", "load-file", "load-library":
		mod = argString
	default:
		return
	}
	if mod == "" {
		return
	}
	result.Edges = append(result.Edges, &graph.Edge{
		From: fileID, To: "unresolved::import::" + mod,
		Kind: graph.EdgeImports, FilePath: filePath, Line: headLine,
	})
}

// isElispDefKeyword reports whether a node type is one of the
// special_form heads that bind a symbol name (defvar/defconst/…).
func isElispDefKeyword(nodeType string) bool {
	switch nodeType {
	case "defvar", "defconst", "defcustom", "defface", "defgroup":
		return true
	}
	return false
}

func isElispKeyword(s string) bool {
	switch s {
	case "if", "when", "unless", "cond", "and", "or", "not", "let", "let*",
		"letrec", "progn", "prog1", "prog2", "lambda", "function", "quote",
		"setq", "setf", "save-excursion", "save-restriction", "while", "dolist",
		"dotimes", "defun", "defmacro", "defvar", "defconst", "defcustom",
		"defface", "defgroup", "require", "provide", "load", "t", "nil":
		return true
	}
	return false
}

var _ parser.Extractor = (*EmacsLispExtractor)(nil)
