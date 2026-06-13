package tstypes

import (
	"strings"

	"github.com/zzet/gortex/internal/graph"
	sitter "github.com/zzet/gortex/internal/parser/tsitter"
	"github.com/zzet/gortex/internal/parser/tsitter/rust"
)

// RustSpec adapts the engine to tree-sitter-rust. An `impl T` block is
// treated as a type scope named T so methods see the struct's fields
// (the field pre-pass keys both the struct_item and the impl blocks on
// the same name); `impl Trait for T` synthesizes an implements edge.
// Bindings come from let annotations, struct expressions (`T { .. }`),
// and the `T::new()` convention — verified against a graph type node
// before any resolution happens through them.
func RustSpec() *LangSpec {
	grammar := rust.GetLanguage()
	return &LangSpec{
		ProviderName: "rust-types",
		Languages:    []string{"rust"},
		GrammarFor:   func(string) *sitter.Language { return grammar },
		TypeDeclTypes: map[string]bool{
			"struct_item": true,
			"enum_item":   true,
			"trait_item":  true,
			"union_item":  true,
			"impl_item":   true,
		},
		FuncDeclTypes: map[string]bool{
			"function_item": true,
		},
		SelfName:     "self",
		TypeDeclName: rustTypeDeclName,
		Supertypes:   rustSupertypes,
		Fields:       rustFields,
		Params:       rustParams,
		ReturnType: func(fn *sitter.Node, src []byte) string {
			return fieldText(fn, "return_type", src)
		},
		LocalBinding: rustLocalBinding,
		Call:         rustCall,
		NewExprType:  rustNewExprType,
		FieldRef: func(n *sitter.Node, src []byte) (string, bool) {
			if n.Type() != "field_expression" {
				return "", false
			}
			val := n.ChildByFieldName("value")
			if val == nil || val.Type() != "self" {
				return "", false
			}
			return fieldText(n, "field", src), true
		},
		Imports: rustImports,
	}
}

func rustTypeDeclName(n *sitter.Node, src []byte) string {
	if n.Type() == "impl_item" {
		t := n.ChildByFieldName("type")
		if t == nil {
			return ""
		}
		return NormalizeTypeName(t.Content(src))
	}
	return fieldText(n, "name", src)
}

func rustSupertypes(n *sitter.Node, src []byte) []SuperRef {
	if n.Type() != "impl_item" {
		return nil
	}
	tr := n.ChildByFieldName("trait")
	if tr == nil {
		return nil
	}
	return []SuperRef{{Name: tr.Content(src), Kind: graph.EdgeImplements, Line: nodeLine(tr)}}
}

func rustFields(n *sitter.Node, src []byte) []Binding {
	if n.Type() != "struct_item" && n.Type() != "union_item" {
		return nil
	}
	body := n.ChildByFieldName("body")
	if body == nil || body.Type() != "field_declaration_list" {
		return nil
	}
	var out []Binding
	for i := 0; i < int(body.NamedChildCount()); i++ {
		c := body.NamedChild(i)
		if c.Type() != "field_declaration" {
			continue
		}
		name := fieldText(c, "name", src)
		if name == "" {
			continue
		}
		out = append(out, Binding{Name: name, Type: fieldText(c, "type", src), Line: nodeLine(c)})
	}
	return out
}

func rustParams(fn *sitter.Node, src []byte) []Binding {
	params := fn.ChildByFieldName("parameters")
	if params == nil {
		return nil
	}
	var out []Binding
	for i := 0; i < int(params.NamedChildCount()); i++ {
		p := params.NamedChild(i)
		if p.Type() != "parameter" {
			continue
		}
		pattern := p.ChildByFieldName("pattern")
		if pattern == nil || pattern.Type() != "identifier" {
			continue
		}
		out = append(out, Binding{Name: pattern.Content(src), Type: fieldText(p, "type", src), Line: nodeLine(p)})
	}
	return out
}

func rustLocalBinding(n *sitter.Node, src []byte) (LocalBind, bool) {
	switch n.Type() {
	case "let_declaration":
		pattern := n.ChildByFieldName("pattern")
		if pattern == nil || pattern.Type() != "identifier" {
			return LocalBind{}, false
		}
		return LocalBind{
			Name:     pattern.Content(src),
			DeclType: fieldText(n, "type", src),
			Init:     n.ChildByFieldName("value"),
		}, true
	case "assignment_expression":
		left := n.ChildByFieldName("left")
		if left == nil || left.Type() != "identifier" {
			return LocalBind{}, false
		}
		return LocalBind{Name: left.Content(src), Init: n.ChildByFieldName("right")}, true
	}
	return LocalBind{}, false
}

func rustCall(n *sitter.Node, src []byte) (*sitter.Node, string, bool) {
	if n.Type() != "call_expression" {
		return nil, "", false
	}
	fn := n.ChildByFieldName("function")
	if fn == nil || fn.Type() != "field_expression" {
		return nil, "", false
	}
	val := fn.ChildByFieldName("value")
	field := fn.ChildByFieldName("field")
	if val == nil || field == nil || field.Type() != "field_identifier" {
		return nil, "", false
	}
	return val, field.Content(src), true
}

// rustNewExprType recognises the two constructor shapes: a struct
// expression `Foo { .. }` and the `Foo::new(..)` convention.
func rustNewExprType(n *sitter.Node, src []byte) string {
	switch n.Type() {
	case "struct_expression":
		if name := n.ChildByFieldName("name"); name != nil {
			return NormalizeTypeName(name.Content(src))
		}
	case "call_expression":
		fn := n.ChildByFieldName("function")
		if fn == nil {
			return ""
		}
		// Foo::new / module::Foo::new / Foo::<T>::new
		if fn.Type() == "scoped_identifier" || fn.Type() == "generic_function" {
			text := fn.Content(src)
			segs := strings.Split(text, "::")
			if len(segs) < 2 || strings.TrimSpace(segs[len(segs)-1]) != "new" {
				return ""
			}
			owner := strings.TrimSpace(segs[len(segs)-2])
			if owner == "" || owner == "Self" {
				return ""
			}
			return NormalizeTypeName(owner)
		}
	}
	return ""
}

func rustImports(root *sitter.Node, src []byte) []Import {
	var out []Import
	var visit func(n *sitter.Node)
	visit = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if n.Type() == "use_declaration" {
			if arg := n.ChildByFieldName("argument"); arg != nil {
				out = append(out, rustUseImports(arg, src, "")...)
			}
			return
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			visit(n.NamedChild(i))
		}
	}
	visit(root)
	return out
}

// rustUseImports flattens a use tree (scoped paths, `as` clauses, and
// brace lists) into local-name bindings. The Path hint is the MODULE
// path — the imported name's parent — because that is what maps onto a
// definition file (`use crate::engine::Svc` lives in engine.rs /
// engine/mod.rs, not in a file named after the type).
func rustUseImports(n *sitter.Node, src []byte, prefix string) []Import {
	switch n.Type() {
	case "identifier", "type_identifier":
		return []Import{{Local: n.Content(src), Path: prefix}}
	case "scoped_identifier":
		full := strings.ReplaceAll(n.Content(src), "::", "/")
		name := full
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		return []Import{{Local: name, Path: joinUsePath(prefix, useParent(full))}}
	case "use_as_clause":
		alias := fieldText(n, "alias", src)
		path := n.ChildByFieldName("path")
		if alias == "" || path == nil {
			return nil
		}
		full := strings.ReplaceAll(path.Content(src), "::", "/")
		return []Import{{Local: alias, Path: joinUsePath(prefix, useParent(full))}}
	case "scoped_use_list":
		path := n.ChildByFieldName("path")
		list := n.ChildByFieldName("list")
		if list == nil {
			return nil
		}
		base := prefix
		if path != nil {
			base = joinUsePath(prefix, strings.ReplaceAll(path.Content(src), "::", "/"))
		}
		var out []Import
		for i := 0; i < int(list.NamedChildCount()); i++ {
			out = append(out, rustUseImports(list.NamedChild(i), src, base)...)
		}
		return out
	case "use_list":
		var out []Import
		for i := 0; i < int(n.NamedChildCount()); i++ {
			out = append(out, rustUseImports(n.NamedChild(i), src, prefix)...)
		}
		return out
	}
	return nil
}

func joinUsePath(prefix, rest string) string {
	for _, tok := range []string{"crate", "self", "super"} {
		rest = strings.TrimPrefix(rest, tok+"/")
		if rest == tok {
			rest = ""
		}
	}
	switch {
	case prefix == "":
		return rest
	case rest == "":
		return prefix
	}
	return prefix + "/" + rest
}

// useParent strips a use path's final segment — the imported name —
// leaving the module path.
func useParent(full string) string {
	if i := strings.LastIndex(full, "/"); i >= 0 {
		return full[:i]
	}
	return ""
}
