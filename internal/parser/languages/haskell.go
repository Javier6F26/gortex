package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// HaskellExtractor extracts Haskell source files using tree-sitter.
//
// Relevant odvcencio grammar nodes:
//   header       ("module" module "where")
//     module       ("Shapes" | "MyLib.Utils" via chained module_id children)
//   imports
//     import       ("import" [qualified] module [as module])
//   data_type    ("data" name "=" data_constructors)
//   newtype      ("newtype" name "=" ...)
//   type_synomym ("type" name "=" ...)
//   class        ("class" name type_params "where" class_declarations)
//   instance     ("instance" name ...)
//   bind         (signature? match)   — top-level function definitions
//   signature    (variable "::" function) — type signature
//
// Variable identifiers (function names) appear as the `variable` leaf;
// type identifiers appear as `name`.
type HaskellExtractor struct {
	lang *sitter.Language
}

func NewHaskellExtractor() *HaskellExtractor {
	return &HaskellExtractor{lang: grammars.HaskellLanguage()}
}

func (e *HaskellExtractor) Language() string     { return "haskell" }
func (e *HaskellExtractor) Extensions() []string { return []string{".hs", ".lhs"} }

func (e *HaskellExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "haskell",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	addNode := func(id string, node *graph.Node) bool {
		if seen[id] {
			return false
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, node)
		return true
	}

	addType := func(name string, line int, typeKind string) {
		if name == "" {
			return
		}
		id := filePath + "::" + name
		if !addNode(id, &graph.Node{
			ID: id, Kind: graph.KindType, Name: name,
			FilePath: filePath, StartLine: line, EndLine: line,
			Language: "haskell", Meta: map[string]any{"type_kind": typeKind},
		}) {
			return
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: line,
		})
	}

	funcNames := make(map[string]bool)
	addFunc := func(name string, startLine, endLine int) {
		if name == "" || isHaskellKeyword(name) {
			return
		}
		id := filePath + "::" + name
		if !addNode(id, &graph.Node{
			ID: id, Kind: graph.KindFunction, Name: name,
			FilePath: filePath, StartLine: startLine, EndLine: endLine,
			Language: "haskell",
		}) {
			return
		}
		funcNames[name] = true
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: startLine,
		})
	}

	// Walk the tree.
	walkNodes(root, func(n *sitter.Node) {
		switch parser.NodeType(n, e.lang) {
		case "header":
			name := e.moduleName(n, src)
			if name == "" {
				return
			}
			id := filePath + "::" + name
			if !addNode(id, &graph.Node{
				ID: id, Kind: graph.KindPackage, Name: name,
				FilePath: filePath, StartLine: 1, EndLine: 1,
				Language: "haskell",
			}) {
				return
			}
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
				FilePath: filePath, Line: 1,
			})

		case "import":
			// First named child of `module` type holds the imported module
			// (dotted name). Second `module` (if present) is the `as` alias.
			mod := e.firstModuleText(n, src)
			if mod != "" {
				result.Edges = append(result.Edges, &graph.Edge{
					From: fileNode.ID, To: "unresolved::import::" + mod,
					Kind: graph.EdgeImports, FilePath: filePath,
					Line: int(n.StartPoint().Row) + 1,
				})
			}

		case "data_type":
			if name, line := haskellNamedChild(n, src, e.lang, "name"); name != "" {
				addType(name, line, "data")
			}
		case "newtype":
			if name, line := haskellNamedChild(n, src, e.lang, "name"); name != "" {
				addType(name, line, "newtype")
			}
		case "type_synomym", "type_synonym":
			if name, line := haskellNamedChild(n, src, e.lang, "name"); name != "" {
				addType(name, line, "type")
			}

		case "class":
			if name, line := haskellNamedChild(n, src, e.lang, "name"); name != "" {
				id := filePath + "::" + name
				if !addNode(id, &graph.Node{
					ID: id, Kind: graph.KindInterface, Name: name,
					FilePath: filePath, StartLine: line, EndLine: line,
					Language: "haskell",
				}) {
					return
				}
				result.Edges = append(result.Edges, &graph.Edge{
					From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
					FilePath: filePath, Line: line,
				})
			}

		case "instance":
			name, line := haskellNamedChild(n, src, e.lang, "name")
			if name == "" {
				return
			}
			id := filePath + "::instance:" + name
			if !addNode(id, &graph.Node{
				ID: id, Kind: graph.KindType, Name: name + " instance",
				FilePath: filePath, StartLine: line, EndLine: line,
				Language: "haskell", Meta: map[string]any{"type_kind": "instance"},
			}) {
				return
			}
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
				FilePath: filePath, Line: line,
			})
			result.Edges = append(result.Edges, &graph.Edge{
				From: id, To: "unresolved::" + name,
				Kind: graph.EdgeImplements, FilePath: filePath, Line: line,
			})
		}
	})

	// Two passes for functions: first from `signature` nodes (type-signed
	// functions), then from top-level `bind` nodes (catches unsigned
	// definitions).
	// Collect top-level signatures and binds at the declarations level.
	walkNodes(root, func(n *sitter.Node) {
		if parser.NodeType(n, e.lang) != "signature" {
			return
		}
		// Only count top-level signatures: parent should be `declarations`
		// or `bind`. Skip those inside `class_declarations`.
		parent := n.Parent()
		if parent != nil && parser.NodeType(parent, e.lang) == "class_declarations" {
			return
		}
		name := e.firstVariable(n, src)
		if name == "" {
			return
		}
		startLine := int(n.StartPoint().Row) + 1
		endLine := int(n.EndPoint().Row) + 1
		addFunc(name, startLine, endLine)
	})
	walkNodes(root, func(n *sitter.Node) {
		if parser.NodeType(n, e.lang) != "bind" {
			return
		}
		name := e.bindName(n, src)
		if name == "" {
			return
		}
		startLine := int(n.StartPoint().Row) + 1
		endLine := int(n.EndPoint().Row) + 1
		addFunc(name, startLine, endLine)
	})

	// Call sites: any `variable` reference inside function bodies
	// emits an unresolved::name call edge (matches the old regex
	// behaviour which scanned for any lowercase identifier).
	funcRanges := buildFuncRanges(result)
	walkNodes(root, func(n *sitter.Node) {
		if parser.NodeType(n, e.lang) != "variable" {
			return
		}
		name := n.Text(src)
		if isHaskellKeyword(name) || len(name) < 2 {
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

// moduleName flattens a `header`'s `module` child into a dotted name.
func (e *HaskellExtractor) moduleName(header *sitter.Node, src []byte) string {
	for i := 0; i < int(header.ChildCount()); i++ {
		c := header.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, e.lang) == "module" && c.IsNamed() {
			return e.flattenModule(c, src)
		}
	}
	return ""
}

// firstModuleText returns the first child of type `module` as a dotted name.
func (e *HaskellExtractor) firstModuleText(n *sitter.Node, src []byte) string {
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, e.lang) == "module" && c.IsNamed() {
			return e.flattenModule(c, src)
		}
	}
	return ""
}

// flattenModule joins the `module_id` children of a `module` node with dots.
func (e *HaskellExtractor) flattenModule(mod *sitter.Node, src []byte) string {
	var parts []string
	for i := 0; i < int(mod.ChildCount()); i++ {
		c := mod.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, e.lang) == "module_id" {
			parts = append(parts, c.Text(src))
		}
	}
	if len(parts) == 0 {
		return strings.TrimSpace(mod.Text(src))
	}
	return strings.Join(parts, ".")
}

// firstVariable returns the text of the first `variable` node in a subtree.
func (e *HaskellExtractor) firstVariable(node *sitter.Node, src []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, e.lang) == "variable" {
			return c.Text(src)
		}
		if name := e.firstVariable(c, src); name != "" {
			return name
		}
	}
	return ""
}

// bindName returns the function name for a `bind` node. It's either in
// the nested `signature` or in an `apply`/`variable` pattern of `match`.
func (e *HaskellExtractor) bindName(bind *sitter.Node, src []byte) string {
	for i := 0; i < int(bind.ChildCount()); i++ {
		c := bind.Child(i)
		if c == nil {
			continue
		}
		switch parser.NodeType(c, e.lang) {
		case "signature":
			if name := e.firstVariable(c, src); name != "" {
				return name
			}
		case "variable":
			return c.Text(src)
		case "apply":
			// First LHS variable under apply is the function name.
			if name := e.firstVariable(c, src); name != "" {
				return name
			}
		case "function":
			if name := e.firstVariable(c, src); name != "" {
				return name
			}
		}
	}
	return ""
}

// haskellNamedChild returns the text and start line of the first direct
// named child with the given node type, or ("", 0) if none.
func haskellNamedChild(n *sitter.Node, src []byte, lang *sitter.Language, nodeType string) (string, int) {
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, lang) == nodeType {
			return c.Text(src), int(c.StartPoint().Row) + 1
		}
	}
	return "", 0
}

func isHaskellKeyword(s string) bool {
	switch s {
	case "module", "where", "import", "qualified", "as", "hiding",
		"data", "newtype", "type", "class", "instance", "deriving",
		"if", "then", "else", "case", "of", "let", "in", "do",
		"return", "forall", "infixl", "infixr", "infix",
		"main":
		return true
	}
	return false
}

var _ parser.Extractor = (*HaskellExtractor)(nil)
