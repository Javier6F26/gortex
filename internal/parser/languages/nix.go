package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// Nix is a lazy functional expression language. The common shapes
// Gortex extracts are attribute bindings at the top level of an attrset
// (`name = expr;`) as variables, lambda-bound names (`name = { a, b }: expr;`
// or `name = a: expr;`) as functions, and imports (`import ./path`,
// `fetchurl { … }`, `builtins.fetchTarball …`). Additionally `with pkgs;`
// emits an import-style edge and `inherit (pkgs.lib) a b` emits a
// references edge to the source.

// NixExtractor extracts Nix expressions with tree-sitter.
type NixExtractor struct {
	lang *sitter.Language
}

func NewNixExtractor() *NixExtractor {
	return &NixExtractor{lang: grammars.NixLanguage()}
}

func (e *NixExtractor) Language() string     { return "nix" }
func (e *NixExtractor) Extensions() []string { return []string{".nix"} }

func (e *NixExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "nix",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)

	// Walk bindings, with-expressions, inherit-from, and apply-expressions
	// (for import/fetch* variants).
	walkNodes(root, func(node *sitter.Node) {
		switch parser.NodeType(node, e.lang) {
		case "binding":
			e.nixHandleBinding(node, src, fileNode.ID, filePath, seen, result)
		case "with_expression":
			e.nixHandleWith(node, src, fileNode.ID, filePath, result)
		case "inherit_from":
			e.nixHandleInheritFrom(node, src, fileNode.ID, filePath, result)
		case "apply_expression":
			e.nixHandleApplyImport(node, src, fileNode.ID, filePath, result)
		}
	})

	return result, nil
}

// nixHandleBinding handles `name = expr;`. If the RHS is a
// function_expression we record a function; otherwise a variable.
func (e *NixExtractor) nixHandleBinding(
	node *sitter.Node, src []byte, fileID, filePath string,
	seen map[string]bool, result *parser.ExtractionResult,
) {
	name := e.nixBindingName(node, src)
	if name == "" || isNixKeyword(name) {
		return
	}
	line := int(node.StartPoint().Row) + 1
	endLine := int(node.EndPoint().Row) + 1

	kind := graph.KindVariable
	if e.nixBindingIsLambda(node) {
		kind = graph.KindFunction
	} else {
		// The old regex-based extractor recorded the variable's end-line
		// as its start-line; preserve that shape so downstream consumers
		// see the same Meta/range footprint.
		endLine = line
	}

	id := filePath + "::" + name
	if seen[id] {
		return
	}
	seen[id] = true
	result.Nodes = append(result.Nodes, &graph.Node{
		ID: id, Kind: kind, Name: name,
		FilePath: filePath, StartLine: line, EndLine: endLine,
		Language: "nix",
	})
	result.Edges = append(result.Edges, &graph.Edge{
		From: fileID, To: id, Kind: graph.EdgeDefines,
		FilePath: filePath, Line: line,
	})
}

// nixBindingName returns the attrpath's first identifier, which is the
// left-hand side of a `name = expr;` binding. Dotted paths (`a.b.c = …`)
// return the leading segment.
func (e *NixExtractor) nixBindingName(node *sitter.Node, src []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, e.lang) != "attrpath" {
			continue
		}
		for j := 0; j < int(c.ChildCount()); j++ {
			gc := c.Child(j)
			if gc == nil {
				continue
			}
			if parser.NodeType(gc, e.lang) == "identifier" {
				return gc.Text(src)
			}
		}
	}
	return ""
}

// nixBindingIsLambda reports whether the binding's RHS is a
// function_expression (i.e. `name = { … }: expr` or `name = arg: expr`).
func (e *NixExtractor) nixBindingIsLambda(node *sitter.Node) bool {
	sawEquals := false
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		t := parser.NodeType(c, e.lang)
		if t == "=" {
			sawEquals = true
			continue
		}
		if !sawEquals {
			continue
		}
		if t == "function_expression" {
			return true
		}
		if t != ";" && !c.IsExtra() {
			return false
		}
	}
	return false
}

// nixHandleWith emits an `import`-kind edge for `with pkgs;`-style prefix
// scoping. Target is the variable or dotted path text (`pkgs`, `pkgs.lib`).
func (e *NixExtractor) nixHandleWith(
	node *sitter.Node, src []byte, fileID, filePath string,
	result *parser.ExtractionResult,
) {
	// Children: `with` keyword, the scoped expression, `;`, body.
	sawKeyword := false
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		t := parser.NodeType(c, e.lang)
		if t == "with" {
			sawKeyword = true
			continue
		}
		if !sawKeyword {
			continue
		}
		if t == ";" {
			break
		}
		target := e.nixExprText(c, src)
		if target == "" {
			continue
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: "unresolved::" + target,
			Kind: graph.EdgeImports, FilePath: filePath,
			Line: int(node.StartPoint().Row) + 1,
		})
		break
	}
}

// nixHandleInheritFrom emits a references-kind edge for `inherit (src) a b`.
// The `src` expression (inside parentheses) is the target; individual attrs
// are the inherited names (not tracked as edges in this pass).
func (e *NixExtractor) nixHandleInheritFrom(
	node *sitter.Node, src []byte, fileID, filePath string,
	result *parser.ExtractionResult,
) {
	// Shape: inherit ( <expr> ) <inherited_attrs> ;
	var target string
	inside := false
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		t := parser.NodeType(c, e.lang)
		switch t {
		case "(":
			inside = true
			continue
		case ")":
			inside = false
			continue
		}
		if inside && target == "" {
			target = e.nixExprText(c, src)
		}
	}
	if target == "" {
		return
	}
	result.Edges = append(result.Edges, &graph.Edge{
		From: fileID, To: "unresolved::" + target,
		Kind: graph.EdgeReferences, FilePath: filePath,
		Line: int(node.StartPoint().Row) + 1,
	})
}

// nixHandleApplyImport recognises `import <expr>`, `builtins.fetchTarball <expr>`,
// `fetchurl <expr>`, `fetchGit <expr>`, `fetchFromGitHub <expr>` and records
// an import edge. apply_expression has the function in the first position
// and the argument in the second.
func (e *NixExtractor) nixHandleApplyImport(
	node *sitter.Node, src []byte, fileID, filePath string,
	result *parser.ExtractionResult,
) {
	if node.NamedChildCount() < 2 {
		return
	}
	callee := node.NamedChild(0)
	arg := node.NamedChild(1)
	if callee == nil || arg == nil {
		return
	}
	calleeText := strings.TrimSpace(callee.Text(src))
	if !isNixImportCallee(calleeText) {
		return
	}

	target := e.nixImportTarget(arg, src)
	if target == "" {
		return
	}
	result.Edges = append(result.Edges, &graph.Edge{
		From: fileID, To: "unresolved::import::" + target,
		Kind: graph.EdgeImports, FilePath: filePath,
		Line: int(node.StartPoint().Row) + 1,
	})
}

// nixImportTarget extracts the textual import target from various
// argument shapes: `<nixpkgs>` (spath_expression), `"./path"`
// (string_expression), `./path` (path_expression), or a plain identifier
// fallback.
func (e *NixExtractor) nixImportTarget(node *sitter.Node, src []byte) string {
	t := parser.NodeType(node, e.lang)
	switch t {
	case "spath_expression":
		// `<nixpkgs>` — strip surrounding angle brackets.
		raw := strings.TrimSpace(node.Text(src))
		return strings.Trim(raw, "<>")
	case "string_expression":
		// Return the inner fragment, stripping quotes.
		for i := 0; i < int(node.ChildCount()); i++ {
			c := node.Child(i)
			if c == nil {
				continue
			}
			if parser.NodeType(c, e.lang) == "string_fragment" {
				return c.Text(src)
			}
		}
		return strings.Trim(node.Text(src), `"'`)
	case "path_expression":
		return strings.TrimSpace(node.Text(src))
	}
	// Fallback: first identifier.
	return firstIdentifier(node, src, e.lang)
}

// nixExprText returns the textual form of a simple expression used in
// `with` / `inherit (…)` positions — bare identifiers, dotted
// select_expressions (e.g. `pkgs.lib`), or parenthesised variants.
func (e *NixExtractor) nixExprText(node *sitter.Node, src []byte) string {
	t := parser.NodeType(node, e.lang)
	switch t {
	case "variable_expression", "identifier":
		return strings.TrimSpace(node.Text(src))
	case "select_expression":
		return strings.TrimSpace(node.Text(src))
	}
	return strings.TrimSpace(node.Text(src))
}

func isNixImportCallee(text string) bool {
	switch text {
	case "import", "builtins.fetchTarball", "fetchurl",
		"fetchGit", "fetchFromGitHub":
		return true
	}
	return false
}

func isNixKeyword(s string) bool {
	switch s {
	case "let", "in", "with", "rec", "if", "then", "else", "true", "false",
		"null", "import", "inherit", "or", "assert", "builtins":
		return true
	}
	return false
}

var _ parser.Extractor = (*NixExtractor)(nil)
