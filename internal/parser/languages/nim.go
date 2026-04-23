package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// Nim keeps the ML-family split between `proc` (effectful), `func` (pure),
// `method` (dispatch), `iterator`, `template`, `macro`, and `converter`.
// The tree-sitter grammar exposes each of these as a `*_declaration` node.
// Types live under `type_section > type_declaration > type_symbol_declaration`.
// Exported symbols have a trailing `*` which the grammar wraps in an
// `exported_symbol` node; we unwrap it to a bare name.

// NimExtractor extracts Nim source with tree-sitter.
type NimExtractor struct {
	lang *sitter.Language
}

func NewNimExtractor() *NimExtractor {
	return &NimExtractor{lang: grammars.NimLanguage()}
}

func (e *NimExtractor) Language() string     { return "nim" }
func (e *NimExtractor) Extensions() []string { return []string{".nim", ".nims", ".nimble"} }

func (e *NimExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	tree, err := parser.ParseFile(src, e.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	root := tree.RootNode()
	result := &parser.ExtractionResult{}

	endLine := int(root.EndPoint().Row) + 1
	if endLine < 1 {
		endLine = 1
	}
	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: endLine,
		Language: "nim",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	add := func(name string, kind graph.NodeKind, start, end int) {
		if name == "" || isNimKeyword(name) {
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
			Language: "nim",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	walkNodes(root, func(node *sitter.Node) {
		switch parser.NodeType(node, e.lang) {
		case "proc_declaration", "func_declaration", "method_declaration",
			"iterator_declaration", "template_declaration", "macro_declaration",
			"converter_declaration":
			name := e.nimRoutineName(node, src)
			add(name, graph.KindFunction,
				int(node.StartPoint().Row)+1, int(node.EndPoint().Row)+1)

		case "type_declaration":
			// A top-level type declaration lives under `type_section`. Its
			// first named child is the type_symbol_declaration holding the
			// name; the name has trailing `*` for exported types. We record
			// as a type regardless of RHS kind (object / enum / tuple / …).
			name := e.nimTypeName(node, src)
			add(name, graph.KindType,
				int(node.StartPoint().Row)+1, int(node.EndPoint().Row)+1)

		case "import_statement", "include_statement", "import_from_statement":
			e.nimAddImports(node, src, fileNode.ID, filePath, result)
		}
	})

	// Call sites inside functions.
	funcRanges := buildFuncRanges(result)
	walkNodes(root, func(node *sitter.Node) {
		if parser.NodeType(node, e.lang) != "call" {
			return
		}
		name := ""
		for i := 0; i < int(node.ChildCount()); i++ {
			c := node.Child(i)
			if c == nil {
				continue
			}
			if parser.NodeType(c, e.lang) == "identifier" {
				name = c.Text(src)
				break
			}
		}
		if name == "" || isNimKeyword(name) {
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

// nimRoutineName extracts the name from a proc/func/method/… declaration.
// Shape: `<keyword> <identifier | exported_symbol(identifier *)> ...`.
func (e *NimExtractor) nimRoutineName(node *sitter.Node, src []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		switch parser.NodeType(c, e.lang) {
		case "identifier":
			return c.Text(src)
		case "exported_symbol":
			return firstIdentifier(c, src, e.lang)
		}
	}
	return ""
}

// nimTypeName extracts the name from a type_declaration.
// Shape: `type_symbol_declaration > (identifier | exported_symbol)`.
func (e *NimExtractor) nimTypeName(node *sitter.Node, src []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, e.lang) != "type_symbol_declaration" {
			continue
		}
		for j := 0; j < int(c.ChildCount()); j++ {
			gc := c.Child(j)
			if gc == nil {
				continue
			}
			switch parser.NodeType(gc, e.lang) {
			case "identifier":
				return gc.Text(src)
			case "exported_symbol":
				return firstIdentifier(gc, src, e.lang)
			}
		}
	}
	return ""
}

// nimAddImports emits import edges for each module referenced in an
// import / include / from … import … statement.
func (e *NimExtractor) nimAddImports(
	node *sitter.Node, src []byte, fileID, filePath string,
	result *parser.ExtractionResult,
) {
	line := int(node.StartPoint().Row) + 1
	emit := func(mod string) {
		mod = strings.TrimSpace(mod)
		if mod == "" {
			return
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: "unresolved::import::" + mod,
			Kind: graph.EdgeImports, FilePath: filePath, Line: line,
		})
	}

	t := parser.NodeType(node, e.lang)
	if t == "import_from_statement" {
		// from M import a, b — emit M only (matches regex behaviour).
		for i := 0; i < int(node.ChildCount()); i++ {
			c := node.Child(i)
			if c == nil {
				continue
			}
			if parser.NodeType(c, e.lang) == "identifier" {
				emit(c.Text(src))
				return
			}
		}
		return
	}

	// import / include: children include `expression_list` holding one or
	// more identifiers.
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		switch parser.NodeType(c, e.lang) {
		case "identifier":
			emit(c.Text(src))
		case "expression_list":
			for j := 0; j < int(c.ChildCount()); j++ {
				gc := c.Child(j)
				if gc == nil {
					continue
				}
				if parser.NodeType(gc, e.lang) == "identifier" {
					emit(gc.Text(src))
				}
			}
		}
	}
}

func isNimKeyword(s string) bool {
	switch s {
	case "if", "elif", "else", "when", "case", "of", "while", "for", "in",
		"notin", "is", "isnot", "block", "break", "continue", "return", "yield",
		"proc", "func", "method", "iterator", "template", "macro", "converter",
		"type", "const", "let", "var", "object", "tuple", "enum", "distinct",
		"ref", "ptr", "import", "include", "from", "as", "export", "defer",
		"try", "except", "finally", "raise", "discard", "true", "false", "nil":
		return true
	}
	return false
}

var _ parser.Extractor = (*NimExtractor)(nil)
