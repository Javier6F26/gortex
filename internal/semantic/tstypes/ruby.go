package tstypes

import (
	"strings"

	"github.com/zzet/gortex/internal/graph"
	sitter "github.com/zzet/gortex/internal/parser/tsitter"
	"github.com/zzet/gortex/internal/parser/tsitter/ruby"
)

// RubySpec adapts the engine to tree-sitter-ruby. Ruby has no type
// annotations, so every binding comes from `Const.new` constructor
// inference — params and bare locals stay unknown and their calls are
// honestly skipped. Mixins (`include` / `extend` / `prepend`) become
// implements edges; `class Foo < Bar` becomes extends.
func RubySpec() *LangSpec {
	grammar := ruby.GetLanguage()
	return &LangSpec{
		ProviderName: "ruby-types",
		Languages:    []string{"ruby"},
		GrammarFor:   func(string) *sitter.Language { return grammar },
		TypeDeclTypes: map[string]bool{
			"class":  true,
			"module": true,
		},
		FuncDeclTypes: map[string]bool{
			"method":           true,
			"singleton_method": true,
		},
		SelfName:     "self",
		TypeDeclName: nameField,
		Supertypes:   rubySupertypes,
		Fields:       rubyFields,
		Params:       rubyParams,
		ReturnType:   nil, // no return annotations in Ruby
		LocalBinding: rubyLocalBinding,
		Call:         rubyCall,
		NewExprType:  rubyNewExprType,
		FieldRef: func(n *sitter.Node, src []byte) (string, bool) {
			if n.Type() != "instance_variable" {
				return "", false
			}
			return n.Content(src), true
		},
		Imports: nil, // require paths don't bind constant names
		// `include M` targets a module, which the ruby extractor
		// indexes as a package node.
		SupertypeKinds: map[graph.NodeKind]bool{
			graph.KindType:      true,
			graph.KindInterface: true,
			graph.KindPackage:   true,
		},
	}
}

func rubySupertypes(n *sitter.Node, src []byte) []SuperRef {
	var out []SuperRef
	if n.Type() == "class" {
		if sup := n.ChildByFieldName("superclass"); sup != nil {
			name := rubyConstantText(sup, src)
			if name != "" {
				out = append(out, SuperRef{Name: name, Kind: graph.EdgeExtends, Line: nodeLine(sup)})
			}
		}
	}
	// `include M` / `extend M` / `prepend M` as direct body statements
	// mix the module's methods in — the closest Ruby has to an
	// implements relation.
	body := rubyBody(n)
	if body == nil {
		return out
	}
	for i := 0; i < int(body.NamedChildCount()); i++ {
		c := body.NamedChild(i)
		if c.Type() != "call" {
			continue
		}
		if c.ChildByFieldName("receiver") != nil {
			continue
		}
		method := fieldText(c, "method", src)
		if method != "include" && method != "extend" && method != "prepend" {
			continue
		}
		args := c.ChildByFieldName("arguments")
		if args == nil {
			continue
		}
		for j := 0; j < int(args.NamedChildCount()); j++ {
			a := args.NamedChild(j)
			if name := rubyConstantText(a, src); name != "" {
				out = append(out, SuperRef{Name: name, Kind: graph.EdgeImplements, Line: nodeLine(a)})
			}
		}
	}
	return out
}

// rubyBody returns a class/module's body_statement node — a `body`
// field in newer grammar revisions, an anonymous child in older ones.
func rubyBody(n *sitter.Node) *sitter.Node {
	if body := n.ChildByFieldName("body"); body != nil {
		return body
	}
	return firstChildOfType(n, "body_statement")
}

// rubyConstantText extracts a constant (or scope-resolved constant)
// name from a node, "" for anything else.
func rubyConstantText(n *sitter.Node, src []byte) string {
	switch n.Type() {
	case "constant":
		return n.Content(src)
	case "scope_resolution", "superclass":
		// take the trailing constant
		for i := int(n.NamedChildCount()) - 1; i >= 0; i-- {
			if c := n.NamedChild(i); c.Type() == "constant" || c.Type() == "scope_resolution" {
				return rubyConstantText(c, src)
			}
		}
	}
	return ""
}

// rubyFields scans the class's method bodies for `@x = Const.new`
// initialisations — the only grounded instance-variable typing
// evidence an annotation-free language offers.
func rubyFields(n *sitter.Node, src []byte) []Binding {
	body := rubyBody(n)
	if body == nil {
		return nil
	}
	var out []Binding
	var visit func(node *sitter.Node)
	visit = func(node *sitter.Node) {
		if node == nil {
			return
		}
		switch node.Type() {
		case "class", "module":
			return // nested types own their ivars
		case "assignment":
			left := node.ChildByFieldName("left")
			right := node.ChildByFieldName("right")
			if left != nil && right != nil && left.Type() == "instance_variable" {
				if typ := rubyNewExprType(right, src); typ != "" {
					out = append(out, Binding{Name: left.Content(src), Type: typ, Line: nodeLine(left)})
				}
			}
		}
		for i := 0; i < int(node.NamedChildCount()); i++ {
			visit(node.NamedChild(i))
		}
	}
	for i := 0; i < int(body.NamedChildCount()); i++ {
		visit(body.NamedChild(i))
	}
	return out
}

func rubyParams(fn *sitter.Node, src []byte) []Binding {
	params := fn.ChildByFieldName("parameters")
	if params == nil {
		return nil
	}
	var out []Binding
	for i := 0; i < int(params.NamedChildCount()); i++ {
		p := params.NamedChild(i)
		name := ""
		switch p.Type() {
		case "identifier":
			name = p.Content(src)
		case "optional_parameter", "keyword_parameter":
			name = fieldText(p, "name", src)
		}
		if name != "" {
			out = append(out, Binding{Name: name, Line: nodeLine(p)})
		}
	}
	return out
}

func rubyLocalBinding(n *sitter.Node, src []byte) (LocalBind, bool) {
	if n.Type() != "assignment" {
		return LocalBind{}, false
	}
	left := n.ChildByFieldName("left")
	if left == nil {
		return LocalBind{}, false
	}
	init := n.ChildByFieldName("right")
	switch left.Type() {
	case "identifier":
		return LocalBind{Name: left.Content(src), Init: init}, true
	case "instance_variable":
		return LocalBind{Name: left.Content(src), Init: init, Field: true}, true
	}
	return LocalBind{}, false
}

// rubyCall decodes `recv.method(...)`, excluding the `Const.new`
// constructor shape (that's NewExprType's job) and mixin keywords.
func rubyCall(n *sitter.Node, src []byte) (*sitter.Node, string, bool) {
	if n.Type() != "call" {
		return nil, "", false
	}
	recv := n.ChildByFieldName("receiver")
	if recv == nil {
		return nil, "", false
	}
	method := fieldText(n, "method", src)
	if method == "" || method == "new" {
		return nil, "", false
	}
	return recv, method, true
}

// rubyNewExprType recognises `Const.new(...)` (and `A::B.new`). The
// receiving constant is the constructed type; the apply phase verifies
// it against a graph type node.
func rubyNewExprType(n *sitter.Node, src []byte) string {
	if n.Type() != "call" {
		return ""
	}
	if fieldText(n, "method", src) != "new" {
		return ""
	}
	recv := n.ChildByFieldName("receiver")
	if recv == nil {
		return ""
	}
	name := rubyConstantText(recv, src)
	if i := strings.LastIndex(name, "::"); i >= 0 {
		name = name[i+2:]
	}
	return name
}
