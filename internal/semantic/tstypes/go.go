package tstypes

import (
	"os/exec"
	"strings"

	sitter "github.com/zzet/gortex/internal/parser/tsitter"
	"github.com/zzet/gortex/internal/parser/tsitter/golang"
)

// GoSpec adapts the engine to tree-sitter-go as a ZERO-TOOLCHAIN
// resolution FLOOR. It is the fallback for the go-types provider: when the
// Go toolchain is installed, go-types (go/packages, go/types) owns Go
// resolution at compiler grade, so this spec suppresses itself entirely
// (Suppressed reports the toolchain present) and contributes nothing. On a
// host without a Go toolchain — where go-types cannot load packages and
// stays silent — this spec supplies receiver-method, constructor-inference
// and typed-variable resolution off the tree-sitter AST alone.
//
// Everything it emits lands at OriginASTResolved (never the lsp_* tiers a
// compiler-grade pass uses), so even were both passes to run on the same
// host, a real go-types edge (lsp_resolved / lsp_dispatch) always outranks
// this engine's floor and is never downgraded by it — the spec is purely
// additive where go-types is silent.
//
// The Go extractor already emits the structure this engine joins against:
// a struct/interface as a KindType node, a method as a KindMethod member of
// its receiver type via EdgeMemberOf, and a constructor function's
// return_type in Meta (pointer stripped). So `x := NewFoo(); x.Bar()` types
// x through NewFoo's return type and resolves x.Bar() to Foo.Bar, and a
// Foo-typed variable / parameter / receiver resolves its calls directly.
func GoSpec() *LangSpec {
	grammar := golang.GetLanguage()
	return &LangSpec{
		ProviderName: "go-ast-types",
		Languages:    []string{"go"},
		GrammarFor:   func(string) *sitter.Language { return grammar },
		Suppressed:   goToolchainPresent,
		// A type declaration's name and body live on the inner type_spec
		// (`type Foo struct {…}` is type_declaration > type_spec), so the
		// engine opens the type scope there.
		TypeDeclTypes: map[string]bool{
			"type_spec": true,
		},
		FuncDeclTypes: map[string]bool{
			"function_declaration": true,
			"method_declaration":   true,
		},
		// Go has no implicit-receiver keyword: a method's receiver is an
		// explicitly named identifier (bound through Params below), so
		// SelfName and FieldRef stay unset.
		TypeDeclName:  nameField,
		Fields:        goFields,
		Params:        goParams,
		ReturnType:    goReturnType,
		LocalBinding:  goLocalBinding,
		Call:          goCall,
		NewExprType:   goNewExprType,
		NormalizeType: goNormalizeType,
	}
}

// goToolchainPresent reports whether the Go toolchain is installed on this
// host. go/packages — the engine go-types drives — requires the `go`
// command on PATH; its presence is therefore the gate that hands Go
// resolution to go-types and suppresses this AST-only floor.
func goToolchainPresent() bool {
	_, err := exec.LookPath("go")
	return err == nil
}

// goNormalizeType reduces a written Go type to the bare identifier the
// graph indexes type nodes under. It strips pointer / address / slice /
// variadic markers and a generic instantiation suffix (`Foo[int]` ->
// `Foo`), but DELIBERATELY keeps a package qualifier intact (`pkg.Foo`
// stays `pkg.Foo`): in-repo type nodes carry bare names, so a qualified
// name fails to resolve and its receiver is conservatively skipped rather
// than mis-bound to a same-named type from another package. Go
// cross-package types are real and not in this engine's reach.
func goNormalizeType(t string) string {
	t = strings.TrimSpace(t)
	for {
		prev := t
		for _, p := range []string{"*", "&", "...", "[]"} {
			t = strings.TrimPrefix(t, p)
		}
		t = strings.TrimSpace(t)
		if t == prev {
			break
		}
	}
	// Generic instantiation `Foo[int]` -> `Foo` (Go uses brackets). Guarded
	// at i > 0 so a leading bracket left by an unhandled shape never zeroes
	// the name.
	if i := strings.IndexByte(t, '['); i > 0 {
		t = t[:i]
	}
	return strings.TrimSpace(t)
}

// goFields lists a struct's declared fields. n is a type_spec; only a
// struct_type body carries fields. Embedded (unnamed) fields contribute no
// binding. A field group `x, y T` yields one binding per name.
func goFields(n *sitter.Node, src []byte) []Binding {
	body := n.ChildByFieldName("type")
	if body == nil || body.Type() != "struct_type" {
		return nil
	}
	list := firstChildOfType(body, "field_declaration_list")
	if list == nil {
		return nil
	}
	var out []Binding
	for fd := range list.NamedChildren() {
		if fd.Type() != "field_declaration" {
			continue
		}
		typ := fieldText(fd, "type", src)
		for c := range fd.NamedChildren() {
			if c.Type() != "field_identifier" {
				continue
			}
			out = append(out, Binding{Name: c.Content(src), Type: typ, Line: nodeLine(c)})
		}
	}
	return out
}

// goParams lists a callable's bound names with their declared types — the
// method receiver first (`func (f *Foo) …` binds f in the body), then the
// regular parameters. A group `a, b T` yields one binding per name. Binding
// the receiver is what lets a method body resolve calls on its own type.
func goParams(fn *sitter.Node, src []byte) []Binding {
	var out []Binding
	if recv := fn.ChildByFieldName("receiver"); recv != nil {
		out = append(out, goParamBindings(recv, src)...)
	}
	if params := fn.ChildByFieldName("parameters"); params != nil {
		out = append(out, goParamBindings(params, src)...)
	}
	return out
}

// goParamBindings flattens a parameter_list into per-name bindings.
func goParamBindings(list *sitter.Node, src []byte) []Binding {
	var out []Binding
	for pd := range list.NamedChildren() {
		if pd.Type() != "parameter_declaration" {
			continue
		}
		typ := fieldText(pd, "type", src)
		for c := range pd.NamedChildren() {
			if c.Type() != "identifier" {
				continue
			}
			out = append(out, Binding{Name: c.Content(src), Type: typ, Line: nodeLine(c)})
		}
	}
	return out
}

// goReturnType extracts a single-type result annotation (`string`, `*Foo`,
// `pkg.T`, `Foo[int]`). A multi-value / named-results signature is a
// parameter_list, not one type, and yields "" so the engine never types a
// receiver through it.
func goReturnType(fn *sitter.Node, src []byte) string {
	res := fn.ChildByFieldName("result")
	if res == nil || res.Type() == "parameter_list" {
		return ""
	}
	return res.Content(src)
}

// goLocalBinding decodes the local declaration / assignment shapes the
// binder folds into the type environment:
//   - short_var_declaration `x := expr` — single name, the initializer
//     drives type inference (constructor / propagation).
//   - var_spec `var x T` / `var x = expr` — explicit annotation when
//     present, else initializer inference.
//   - assignment_statement `x = expr` — surfaced so a reassignment to an
//     unprovable type poisons the binding (single-assignment-lite), keeping
//     the engine from resolving through a stale type.
//
// Multi-name / multi-value forms are skipped (they bind no single defensible
// type), so the engine stays conservative.
func goLocalBinding(n *sitter.Node, src []byte) (LocalBind, bool) {
	switch n.Type() {
	case "short_var_declaration", "assignment_statement":
		name := soleIdent(n.ChildByFieldName("left"))
		if name == nil {
			return LocalBind{}, false
		}
		lb := LocalBind{Name: name.Content(src)}
		if init := soleExpr(n.ChildByFieldName("right")); init != nil {
			lb.Init = init
		}
		return lb, true
	case "var_spec":
		name := n.ChildByFieldName("name")
		if name == nil || name.Type() != "identifier" {
			return LocalBind{}, false
		}
		lb := LocalBind{Name: name.Content(src), DeclType: fieldText(n, "type", src)}
		if init := soleExpr(n.ChildByFieldName("value")); init != nil {
			lb.Init = init
		}
		return lb, true
	}
	return LocalBind{}, false
}

// soleIdent returns the single identifier of an expression_list, nil when
// the list is absent, holds more than one element, or that element is not a
// bare identifier (the multi-assignment forms the engine declines to type).
func soleIdent(list *sitter.Node) *sitter.Node {
	if list == nil || list.NamedChildCount() != 1 {
		return nil
	}
	c := list.NamedChild(0)
	if c == nil || c.Type() != "identifier" {
		return nil
	}
	return c
}

// soleExpr returns the single expression of an expression_list, nil unless
// exactly one is present.
func soleExpr(list *sitter.Node) *sitter.Node {
	if list == nil || list.NamedChildCount() != 1 {
		return nil
	}
	return list.NamedChild(0)
}

// goCall decodes a receiver-qualified call `recv.method(args)`: a
// call_expression whose function is a selector_expression. A receiverless
// call (`NewFoo()` — function is a bare identifier) returns ok=false; those
// are the resolver's job, and the engine reads them only as receiver
// initializers via the bare-callee inference path.
func goCall(n *sitter.Node, src []byte) (*sitter.Node, string, bool) {
	if n.Type() != "call_expression" {
		return nil, "", false
	}
	fn := n.ChildByFieldName("function")
	if fn == nil || fn.Type() != "selector_expression" {
		return nil, "", false
	}
	recv := fn.ChildByFieldName("operand")
	field := fn.ChildByFieldName("field")
	if recv == nil || field == nil {
		return nil, "", false
	}
	return recv, field.Content(src), true
}

// goNewExprType returns the constructed type name when n is a composite
// literal (`Foo{…}`), an address-of composite literal (`&Foo{…}`), or a
// `new(T)` allocation. "" otherwise — a `NewFoo()`-style constructor call is
// a plain call, typed instead through its return type by the bare-callee
// path.
func goNewExprType(n *sitter.Node, src []byte) string {
	switch n.Type() {
	case "composite_literal":
		return fieldText(n, "type", src)
	case "unary_expression":
		if op := n.ChildByFieldName("operand"); op != nil && op.Type() == "composite_literal" {
			return fieldText(op, "type", src)
		}
	case "call_expression":
		fn := n.ChildByFieldName("function")
		if fn == nil || fn.Type() != "identifier" || fn.Content(src) != "new" {
			return ""
		}
		if args := n.ChildByFieldName("arguments"); args != nil && args.NamedChildCount() >= 1 {
			if first := args.NamedChild(0); first != nil {
				return first.Content(src)
			}
		}
	}
	return ""
}
