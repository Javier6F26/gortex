package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// Solidity is the dominant smart-contract language for the EVM. The
// odvcencio grammar surfaces well-formed top-level nodes:
//   - `contract_declaration` / `interface_declaration` (plus `library`
//     / `abstract contract`: the same grammar rule with an `abstract`
//     keyword child).
//   - Inside a contract_body: `function_definition`,
//     `modifier_definition`, `event_definition`, `struct_declaration`,
//     `enum_declaration`, `state_variable_declaration`.
//   - `import_directive` with a `string` child holding the quoted
//     path.
// Calls: `call_expression` nodes whose first child is an `expression`
// wrapping an identifier.

// SolidityExtractor extracts Solidity source using tree-sitter.
type SolidityExtractor struct {
	lang *sitter.Language
}

func NewSolidityExtractor() *SolidityExtractor {
	return &SolidityExtractor{lang: grammars.SolidityLanguage()}
}

func (e *SolidityExtractor) Language() string     { return "solidity" }
func (e *SolidityExtractor) Extensions() []string { return []string{".sol"} }

func (e *SolidityExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	tree, err := parser.ParseFile(src, e.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	root := tree.RootNode()
	result := &parser.ExtractionResult{}
	lines := strings.Split(string(src), "\n")

	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: len(lines),
		Language: "solidity",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	add := func(name string, kind graph.NodeKind, start, end int, meta map[string]any) {
		if name == "" || isSolidityKeyword(name) {
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
			Language: "solidity",
			Meta:     meta,
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

	walkNodes(root, func(node *sitter.Node) {
		nt := parser.NodeType(node, e.lang)
		switch nt {
		case "contract_declaration":
			// `contract X` or `abstract contract X` or `library X`
			name := e.firstIdentifier(node, src)
			start := int(node.StartPoint().Row) + 1
			end := int(node.EndPoint().Row) + 1
			kw := e.contractKeyword(node, src)
			add(name, graph.KindType, start, end, map[string]any{"sol_kind": kw})
		case "library_declaration":
			name := e.firstIdentifier(node, src)
			start := int(node.StartPoint().Row) + 1
			end := int(node.EndPoint().Row) + 1
			add(name, graph.KindType, start, end, map[string]any{"sol_kind": "library"})
		case "interface_declaration":
			name := e.firstIdentifier(node, src)
			start := int(node.StartPoint().Row) + 1
			end := int(node.EndPoint().Row) + 1
			add(name, graph.KindInterface, start, end, map[string]any{"sol_kind": "interface"})
		case "struct_declaration":
			name := e.firstIdentifier(node, src)
			start := int(node.StartPoint().Row) + 1
			end := int(node.EndPoint().Row) + 1
			add(name, graph.KindType, start, end, map[string]any{"sol_kind": "struct"})
		case "enum_declaration":
			name := e.firstIdentifier(node, src)
			start := int(node.StartPoint().Row) + 1
			end := int(node.EndPoint().Row) + 1
			add(name, graph.KindType, start, end, map[string]any{"sol_kind": "enum"})
		case "function_definition":
			name := e.firstIdentifier(node, src)
			start := int(node.StartPoint().Row) + 1
			end := int(node.EndPoint().Row) + 1
			add(name, graph.KindMethod, start, end, nil)
		case "modifier_definition":
			name := e.firstIdentifier(node, src)
			start := int(node.StartPoint().Row) + 1
			end := int(node.EndPoint().Row) + 1
			add(name, graph.KindMethod, start, end, map[string]any{"sol_kind": "modifier"})
		case "event_definition":
			name := e.firstIdentifier(node, src)
			start := int(node.StartPoint().Row) + 1
			add(name, graph.KindMethod, start, start, map[string]any{"sol_kind": "event"})
		case "state_variable_declaration":
			name := e.stateVariableName(node, src)
			start := int(node.StartPoint().Row) + 1
			add(name, graph.KindVariable, start, start, nil)
		case "import_directive":
			path := e.importPath(node, src)
			addImport(path, int(node.StartPoint().Row)+1)
		}
	})

	// Call-site edges inside functions/modifiers/events.
	funcRanges := buildFuncRanges(result)
	walkNodes(root, func(node *sitter.Node) {
		if parser.NodeType(node, e.lang) != "call_expression" {
			return
		}
		name := e.callHead(node, src)
		if name == "" || isSolidityKeyword(name) || isSolidityType(strings.ToLower(name)) {
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

// firstIdentifier returns the first direct `identifier` child's text.
func (e *SolidityExtractor) firstIdentifier(node *sitter.Node, src []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, e.lang) == "identifier" {
			return strings.TrimSpace(c.Text(src))
		}
	}
	return ""
}

// contractKeyword returns which keyword introduced a
// contract_declaration: `contract`, `library`, or `abstract contract`.
// The grammar puts these as bare keyword children before the identifier.
func (e *SolidityExtractor) contractKeyword(node *sitter.Node, src []byte) string {
	var parts []string
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		t := parser.NodeType(c, e.lang)
		switch t {
		case "abstract", "contract", "library":
			parts = append(parts, t)
		case "identifier":
			// Stop at the name.
			if len(parts) == 0 {
				// Fallback: inspect raw text up to the identifier start.
				return strings.TrimSpace(
					strings.SplitN(node.Text(src), c.Text(src), 2)[0])
			}
			return strings.Join(parts, " ")
		}
	}
	return strings.Join(parts, " ")
}

// stateVariableName finds the variable name in a state variable
// declaration. The grammar shape is:
//
//	state_variable_declaration → type_name, visibility?, identifier, ;
func (e *SolidityExtractor) stateVariableName(node *sitter.Node, src []byte) string {
	// The name is the last `identifier` direct child (earlier
	// `identifier` children can appear inside nested type_names for
	// user-defined types).
	var last string
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, e.lang) == "identifier" {
			last = strings.TrimSpace(c.Text(src))
		}
	}
	return last
}

// importPath returns the unquoted path string of an import_directive.
func (e *SolidityExtractor) importPath(node *sitter.Node, src []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, e.lang) == "string" {
			raw := c.Text(src)
			return strings.Trim(strings.TrimSpace(raw), "\"'")
		}
	}
	return ""
}

// callHead extracts the identifier at the head of a call_expression.
// The Solidity grammar wraps the head in an `expression` node which
// contains either an `identifier`, a `primitive_type` (e.g. address(0)),
// or a `member_expression` whose last property identifier is the name.
func (e *SolidityExtractor) callHead(node *sitter.Node, src []byte) string {
	if node == nil {
		return ""
	}
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		t := parser.NodeType(c, e.lang)
		if t == "expression" {
			return e.unwrapExpressionHead(c, src)
		}
		if t == "identifier" {
			return strings.TrimSpace(c.Text(src))
		}
	}
	return ""
}

func (e *SolidityExtractor) unwrapExpressionHead(node *sitter.Node, src []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		t := parser.NodeType(c, e.lang)
		switch t {
		case "identifier":
			return strings.TrimSpace(c.Text(src))
		case "member_expression":
			// last identifier child is the method name
			for j := int(c.ChildCount()) - 1; j >= 0; j-- {
				inner := c.Child(j)
				if inner == nil {
					continue
				}
				if parser.NodeType(inner, e.lang) == "identifier" {
					return strings.TrimSpace(inner.Text(src))
				}
			}
		case "primitive_type":
			return strings.TrimSpace(c.Text(src))
		}
	}
	return ""
}

func isSolidityKeyword(s string) bool {
	switch s {
	case "if", "else", "for", "while", "do", "break", "continue", "return",
		"function", "modifier", "event", "struct", "enum", "contract",
		"library", "interface", "abstract", "pragma", "import", "using",
		"new", "delete", "emit", "payable", "view", "pure", "public",
		"private", "internal", "external", "memory", "storage", "calldata",
		"require", "assert", "revert", "assembly", "true", "false", "null":
		return true
	}
	return false
}

func isSolidityType(s string) bool {
	if strings.HasPrefix(s, "uint") || strings.HasPrefix(s, "int") ||
		strings.HasPrefix(s, "bytes") {
		return true
	}
	switch s {
	case "address", "bool", "string", "bytes", "byte", "mapping":
		return true
	}
	return false
}

var _ parser.Extractor = (*SolidityExtractor)(nil)
