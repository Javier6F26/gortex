package tstypes

import (
	"strings"
	"unicode"

	"github.com/zzet/gortex/internal/graph"
	sitter "github.com/zzet/gortex/internal/parser/tsitter"
	"github.com/zzet/gortex/internal/parser/tsitter/python"
)

// PythonSpec adapts the engine to tree-sitter-python. Typing evidence
// comes from PEP-484 annotations (params, locals, returns) and from
// CapWords constructor calls (`x = Foo()`); the latter are
// convention-based, so the engine's apply phase only acts when the
// name resolves to a real graph class node. `self.x` attributes bind
// in the class scope; explicit base classes synthesize extends edges.
func PythonSpec() *LangSpec {
	grammar := python.GetLanguage()
	return &LangSpec{
		ProviderName: "python-types",
		Languages:    []string{"python"},
		GrammarFor:   func(string) *sitter.Language { return grammar },
		TypeDeclTypes: map[string]bool{
			"class_definition": true,
		},
		FuncDeclTypes: map[string]bool{
			"function_definition": true,
		},
		SelfName:     "self",
		TypeDeclName: nameField,
		Supertypes:   pySupertypes,
		Fields:       pyFields,
		Params:       pyParams,
		ReturnType: func(fn *sitter.Node, src []byte) string {
			return fieldText(fn, "return_type", src)
		},
		LocalBinding: pyLocalBinding,
		Call:         pyCall,
		NewExprType:  pyNewExprType,
		FieldRef: func(n *sitter.Node, src []byte) (string, bool) {
			if n.Type() != "attribute" {
				return "", false
			}
			obj := n.ChildByFieldName("object")
			if obj == nil || obj.Type() != "identifier" || obj.Content(src) != "self" {
				return "", false
			}
			return fieldText(n, "attribute", src), true
		},
		Imports: pyImports,
	}
}

func pySupertypes(n *sitter.Node, src []byte) []SuperRef {
	supers := n.ChildByFieldName("superclasses")
	if supers == nil {
		return nil
	}
	var out []SuperRef
	for c := range supers.NamedChildren() {
		switch c.Type() {
		case "identifier", "attribute":
			name := c.Content(src)
			// Drop the typing protocol scaffolding bases — they carry
			// no resolvable repo-local definition.
			bare := name
			if i := strings.LastIndex(bare, "."); i >= 0 {
				bare = bare[i+1:]
			}
			if bare == "object" {
				continue
			}
			out = append(out, SuperRef{Name: name, Kind: graph.EdgeExtends, Line: nodeLine(c)})
		}
	}
	return out
}

// pyFields collects class-level annotated assignments and `self.x`
// initialisations inside method bodies.
func pyFields(n *sitter.Node, src []byte) []Binding {
	body := n.ChildByFieldName("body")
	if body == nil {
		return nil
	}
	var out []Binding
	var visit func(node *sitter.Node, selfOnly bool)
	visit = func(node *sitter.Node, selfOnly bool) {
		if node == nil {
			return
		}
		if node.Type() == "class_definition" {
			return // nested classes own their fields
		}
		if node.Type() == "assignment" {
			left := node.ChildByFieldName("left")
			typ := fieldText(node, "type", src)
			if typ == "" {
				if right := node.ChildByFieldName("right"); right != nil {
					typ = pyNewExprType(right, src)
				}
			}
			if left != nil && typ != "" {
				switch left.Type() {
				case "identifier":
					// Class-level statement: a class attribute. Inside a
					// method body the same shape is a local — skip there.
					if !selfOnly {
						out = append(out, Binding{Name: left.Content(src), Type: typ, Line: nodeLine(left)})
					}
				case "attribute":
					obj := left.ChildByFieldName("object")
					if obj != nil && obj.Type() == "identifier" && obj.Content(src) == "self" {
						out = append(out, Binding{Name: fieldText(left, "attribute", src), Type: typ, Line: nodeLine(left)})
					}
				}
			}
		}
		for child := range node.NamedChildren() {
			visit(child, selfOnly)
		}
	}
	// Class-level: only direct statements; self.x: walk method bodies.
	for c := range body.NamedChildren() {
		switch c.Type() {
		case "expression_statement":
			visit(c, false)
		case "function_definition":
			if fb := c.ChildByFieldName("body"); fb != nil {
				visit(fb, true)
			}
		}
	}
	return out
}

func pyParams(fn *sitter.Node, src []byte) []Binding {
	params := fn.ChildByFieldName("parameters")
	if params == nil {
		return nil
	}
	var out []Binding
	for p := range params.NamedChildren() {
		switch p.Type() {
		case "identifier":
			out = append(out, Binding{Name: p.Content(src), Line: nodeLine(p)})
		case "typed_parameter":
			var name string
			if id := firstChildOfType(p, "identifier"); id != nil {
				name = id.Content(src)
			}
			if name == "" {
				continue
			}
			out = append(out, Binding{Name: name, Type: fieldText(p, "type", src), Line: nodeLine(p)})
		case "default_parameter":
			name := fieldText(p, "name", src)
			if name == "" {
				continue
			}
			out = append(out, Binding{Name: name, Line: nodeLine(p)})
		case "typed_default_parameter":
			name := fieldText(p, "name", src)
			if name == "" {
				continue
			}
			out = append(out, Binding{Name: name, Type: fieldText(p, "type", src), Line: nodeLine(p)})
		}
	}
	return out
}

func pyLocalBinding(n *sitter.Node, src []byte) (LocalBind, bool) {
	if n.Type() != "assignment" {
		return LocalBind{}, false
	}
	left := n.ChildByFieldName("left")
	if left == nil {
		return LocalBind{}, false
	}
	declType := fieldText(n, "type", src)
	init := n.ChildByFieldName("right")
	switch left.Type() {
	case "identifier":
		return LocalBind{Name: left.Content(src), DeclType: declType, Init: init}, true
	case "attribute":
		obj := left.ChildByFieldName("object")
		if obj != nil && obj.Type() == "identifier" && obj.Content(src) == "self" {
			return LocalBind{Name: fieldText(left, "attribute", src), DeclType: declType, Init: init, Field: true}, true
		}
	}
	return LocalBind{}, false
}

func pyCall(n *sitter.Node, src []byte) (*sitter.Node, string, bool) {
	if n.Type() != "call" {
		return nil, "", false
	}
	fn := n.ChildByFieldName("function")
	if fn == nil || fn.Type() != "attribute" {
		return nil, "", false
	}
	obj := fn.ChildByFieldName("object")
	if obj == nil {
		return nil, "", false
	}
	return obj, fieldText(fn, "attribute", src), true
}

// pyNewExprType treats `Foo(...)` as a constructor candidate when the
// callee follows the CapWords class convention. The apply phase still
// verifies the name against a real graph type node before resolving
// anything through it, so a capitalized factory function never grounds
// a false receiver.
func pyNewExprType(n *sitter.Node, src []byte) string {
	if n.Type() != "call" {
		return ""
	}
	fn := n.ChildByFieldName("function")
	if fn == nil || fn.Type() != "identifier" {
		return ""
	}
	name := fn.Content(src)
	if name == "" {
		return ""
	}
	r := []rune(name)
	if !unicode.IsUpper(r[0]) {
		return ""
	}
	return name
}

func pyImports(root *sitter.Node, src []byte) []Import {
	var out []Import
	var visit func(n *sitter.Node)
	visit = func(n *sitter.Node) {
		if n == nil {
			return
		}
		switch n.Type() {
		case "import_from_statement":
			module := fieldText(n, "module_name", src)
			if module == "" {
				return
			}
			path := strings.ReplaceAll(module, ".", "/")
			for c := range n.NamedChildren() {
				switch c.Type() {
				case "dotted_name":
					if c.Content(src) == module {
						continue // the module_name child itself
					}
					out = append(out, Import{Local: c.Content(src), Path: path})
				case "aliased_import":
					alias := fieldText(c, "alias", src)
					name := fieldText(c, "name", src)
					if alias == "" {
						alias = name
					}
					if alias != "" {
						out = append(out, Import{Local: alias, Path: path})
					}
				}
			}
		case "import_statement":
			// `import a.b` binds the package root, not a class name —
			// only `import x as y` introduces a flat local binding,
			// and that's a module, not a type. Skip.
		default:
			for child := range n.NamedChildren() {
				visit(child)
			}
		}
	}
	visit(root)
	return out
}
