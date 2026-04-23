package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// R's tree-sitter grammar models assignments as a generic
// `binary_operator` whose operator child is `<-`, `=`, `->`, or `<<-`.
// A "function definition" is that same node with a `function_definition`
// on the right-hand side. Library/require/source are plain `call`s
// with a well-known identifier head.

// RExtractor extracts R source files using tree-sitter.
type RExtractor struct {
	lang *sitter.Language
}

func NewRExtractor() *RExtractor {
	return &RExtractor{lang: grammars.RLanguage()}
}

func (e *RExtractor) Language() string     { return "r" }
func (e *RExtractor) Extensions() []string { return []string{".R", ".r", ".Rmd"} }

func (e *RExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "r",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	add := func(name string, kind graph.NodeKind, start, end int) {
		if name == "" || isRKeyword(name) {
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
			Language: "r",
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

	// Walk only top-level children of the program for definitions; the
	// regex-based original only looked at line-starting forms.
	for i := 0; i < int(root.ChildCount()); i++ {
		child := root.Child(i)
		if child == nil {
			continue
		}
		switch parser.NodeType(child, e.lang) {
		case "binary_operator":
			name, op, rhsKind := e.assignmentShape(child, src)
			if name == "" {
				continue
			}
			if !isRAssignOp(op) {
				continue
			}
			start := int(child.StartPoint().Row) + 1
			end := int(child.EndPoint().Row) + 1
			if rhsKind == "function_definition" {
				add(name, graph.KindFunction, start, findBlockEnd(lines, start))
			} else {
				add(name, graph.KindVariable, start, start)
				_ = end
			}
		case "call":
			// library(x), require(x), source("file.R")
			head, arg, line := e.callShape(child, src)
			switch head {
			case "library", "require":
				addImport(arg, line)
			case "source":
				addImport(arg, line)
			}
		}
	}

	// Call-site edges inside any function: walk entire tree for `call`
	// nodes and attribute them to the enclosing function by line range.
	funcRanges := buildFuncRanges(result)
	walkNodes(root, func(node *sitter.Node) {
		if parser.NodeType(node, e.lang) != "call" {
			return
		}
		head, _, line := e.callShape(node, src)
		if head == "" || isRKeyword(head) || head == "function" {
			return
		}
		callerID := findEnclosingFunc(funcRanges, line)
		if callerID == "" || strings.HasSuffix(callerID, "::"+head) {
			return
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: callerID, To: "unresolved::" + head,
			Kind: graph.EdgeCalls, FilePath: filePath, Line: line,
		})
	})

	return result, nil
}

// assignmentShape returns (lhsName, operatorText, rhsGrammarType).
// A binary_operator with a function child means a function
// definition. R's `<-`, `=`, and `->` can all introduce bindings; we
// only treat left-to-right forms (`name <- value`, `name = value`) as
// definitions to match the original regex behaviour.
func (e *RExtractor) assignmentShape(node *sitter.Node, src []byte) (string, string, string) {
	var lhs, op, rhsType string
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		t := parser.NodeType(c, e.lang)
		switch {
		case lhs == "" && t == "identifier":
			lhs = c.Text(src)
		case op == "" && isRAssignOp(t):
			op = t
		case op != "" && rhsType == "":
			rhsType = t
		}
	}
	return lhs, op, rhsType
}

func isRAssignOp(t string) bool {
	switch t {
	case "<-", "=", "<<-":
		return true
	}
	return false
}

// callShape extracts the identifier head of a call node plus the
// first argument's string content when it is a string literal (for
// source("utils.R")) or its identifier text (library(pkg)).
func (e *RExtractor) callShape(node *sitter.Node, src []byte) (head, arg string, line int) {
	line = int(node.StartPoint().Row) + 1
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		t := parser.NodeType(c, e.lang)
		if head == "" && t == "identifier" {
			head = c.Text(src)
			continue
		}
		if t == "arguments" {
			arg = firstCallArg(c, src, e.lang)
		}
	}
	return head, arg, line
}

// firstCallArg walks an arguments node and returns the text of the
// first argument. String literals surface their unquoted content; bare
// identifiers (library(pkg)) surface their text verbatim.
func firstCallArg(argsNode *sitter.Node, src []byte, lang *sitter.Language) string {
	for i := 0; i < int(argsNode.ChildCount()); i++ {
		c := argsNode.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, lang) != "argument" {
			continue
		}
		// `argument` → identifier | string | …
		for j := 0; j < int(c.ChildCount()); j++ {
			inner := c.Child(j)
			if inner == nil {
				continue
			}
			t := parser.NodeType(inner, lang)
			switch t {
			case "identifier":
				return inner.Text(src)
			case "string":
				return stripRString(inner, src, lang)
			}
		}
	}
	return ""
}

// stripRString extracts the content of a string node. The R grammar
// nests a `string_content` named child between the two quote
// terminals; fall back to trimming quotes if that child is missing.
func stripRString(node *sitter.Node, src []byte, lang *sitter.Language) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, lang) == "string_content" {
			return c.Text(src)
		}
	}
	return strings.Trim(node.Text(src), "\"'")
}

func isRKeyword(s string) bool {
	switch s {
	case "if", "else", "for", "while", "repeat", "in", "next", "break",
		"return", "function", "TRUE", "FALSE", "NULL", "NA", "Inf", "NaN",
		"library", "require", "source":
		return true
	}
	return false
}

var _ parser.Extractor = (*RExtractor)(nil)
