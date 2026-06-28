package tstypes

import (
	"strings"
	"unicode"

	"github.com/zzet/gortex/internal/graph"
	sitter "github.com/zzet/gortex/internal/parser/tsitter"
	"github.com/zzet/gortex/internal/parser/tsitter/kotlin"
)

// KotlinSpec adapts the engine to tree-sitter-kotlin. Kotlin is a
// statically typed OO language like Java, with three syntactic quirks the
// hooks decode: a primary-constructor `val`/`var` parameter is BOTH a
// constructor parameter and a class property (`class C(val dep: Foo)`),
// construction takes no `new` keyword (a `Foo()` call whose callee is a
// type name constructs `Foo`), and the supertype list does not
// syntactically discriminate the base class from interfaces — those
// SuperRefs carry an empty Kind and the apply phase decides by the
// resolved node's kind, exactly like C#. Class / interface declarations
// are both `class_declaration` (an interface carries an `interface`
// keyword child); objects are `object_declaration`.
func KotlinSpec() *LangSpec {
	grammar := kotlin.GetLanguage()
	return &LangSpec{
		ProviderName: "kotlin-types",
		Languages:    []string{"kotlin"},
		GrammarFor:   func(string) *sitter.Language { return grammar },
		TypeDeclTypes: map[string]bool{
			"class_declaration":  true,
			"object_declaration": true,
		},
		FuncDeclTypes: map[string]bool{
			"function_declaration":  true,
			"secondary_constructor": true,
		},
		SelfName:     "this",
		TypeDeclName: kotlinTypeName,
		Supertypes:   kotlinSupertypes,
		Fields:       kotlinFields,
		Params:       kotlinParams,
		ReturnType:   kotlinReturnType,
		LocalBinding: kotlinLocalBinding,
		Call:         kotlinCall,
		NewExprType:  kotlinNewExprType,
		FieldRef:     kotlinFieldRef,
		Imports:      kotlinImports,
		// A class supertype is reached through EdgeExtends and an
		// interface supertype through EdgeImplements; widen the
		// inherited-member climb to both so an interface default method,
		// or a base-class method, both resolve. Ambiguity across two
		// ancestors stays unresolved, never half-guessed.
		InheritEdgeKinds: []graph.EdgeKind{graph.EdgeExtends, graph.EdgeImplements},
		NormalizeType:    normalizeKotlinType,
		// Kotlin top-level `fun Foo.ext()` is an extension function: callable
		// as `foo.ext()` on any Foo receiver. The extractor stamps the
		// receiver on Meta["extension_receiver"]; this opts the call phase
		// into resolving such calls against the receiver type, with real
		// members winning over extensions.
		ExtensionFunctions: true,
	}
}

// kotlinTypeName returns the declared name of a class / object / interface
// declaration: its `type_identifier` child. tree-sitter-kotlin carries no
// `name` field, so the shared nameField helper does not apply.
func kotlinTypeName(n *sitter.Node, src []byte) string {
	if c := firstChildOfType(n, "type_identifier"); c != nil {
		return c.Content(src)
	}
	return ""
}

// kotlinSupertypes lists the declared supertypes of a class / object: each
// `delegation_specifier` in the `: Base(), Iface` list. A class supertype
// appears as a `constructor_invocation` (`Base()`, the super-constructor
// call); an interface as a bare `user_type` (`Iface`). The relation kind is
// deferred (empty) — the apply phase resolves the name and chooses
// EdgeExtends for a class target and EdgeImplements for an interface, which
// is strictly more reliable than guessing from the presence of parentheses.
func kotlinSupertypes(n *sitter.Node, src []byte) []SuperRef {
	var out []SuperRef
	for c := range n.NamedChildren() {
		if c.Type() != "delegation_specifier" {
			continue
		}
		var ut *sitter.Node
		if ci := firstChildOfType(c, "constructor_invocation"); ci != nil {
			ut = firstChildOfType(ci, "user_type")
		} else {
			ut = firstChildOfType(c, "user_type")
		}
		if ut == nil {
			continue
		}
		name := strings.TrimSpace(ut.Content(src))
		if name == "" {
			continue
		}
		out = append(out, SuperRef{Name: name, Kind: graph.EdgeKind(""), Line: nodeLine(c)})
	}
	return out
}

// kotlinFields grounds the instance-field types of a Kotlin type. Two
// sources contribute: primary-constructor `val`/`var` parameters
// (`class C(val dep: Foo)` — a parameter that is also a property, marked by
// a binding_pattern_kind child), and class-body property declarations
// (`val x: Foo`, plus the `val x = Foo()` constructor-initialised form). A
// plain primary-constructor parameter with no `val`/`var` is a constructor
// local, not a property, and is skipped.
func kotlinFields(n *sitter.Node, src []byte) []Binding {
	var out []Binding
	if pc := firstChildOfType(n, "primary_constructor"); pc != nil {
		for c := range pc.NamedChildren() {
			if c.Type() != "class_parameter" {
				continue
			}
			// `val`/`var` makes the parameter a property; without it the
			// parameter is constructor-local and not a field.
			if firstChildOfType(c, "binding_pattern_kind") == nil {
				continue
			}
			name := kotlinSimpleIdent(c, src)
			typ := kotlinUserTypeText(c, src)
			if name == "" || typ == "" {
				continue
			}
			out = append(out, Binding{Name: name, Type: typ, Line: nodeLine(c)})
		}
	}
	body := firstChildOfType(n, "class_body")
	if body == nil {
		body = firstChildOfType(n, "enum_class_body")
	}
	if body != nil {
		for c := range body.NamedChildren() {
			if c.Type() != "property_declaration" {
				continue
			}
			vd := firstChildOfType(c, "variable_declaration")
			if vd == nil {
				continue
			}
			name := kotlinSimpleIdent(vd, src)
			if name == "" {
				continue
			}
			typ := kotlinUserTypeText(vd, src)
			if typ == "" {
				// `val x = Foo()` — a Capitalized constructor call types the
				// property even without an explicit annotation.
				if init := kotlinNamedChildAfter(c, vd); init != nil {
					typ = kotlinNewExprType(init, src)
				}
			}
			if typ == "" {
				continue
			}
			out = append(out, Binding{Name: name, Type: typ, Line: nodeLine(c)})
		}
	}
	return out
}

// kotlinParams lists a callable's parameters: each `parameter` in the
// `function_value_parameters` list, as `name: Type`. Shared by
// function_declaration and secondary_constructor, which use the same
// parameter shape. The throwaway `_` name is skipped.
func kotlinParams(fn *sitter.Node, src []byte) []Binding {
	params := firstChildOfType(fn, "function_value_parameters")
	if params == nil {
		return nil
	}
	var out []Binding
	for p := range params.NamedChildren() {
		if p.Type() != "parameter" {
			continue
		}
		name := kotlinSimpleIdent(p, src)
		if name == "" || name == "_" {
			continue
		}
		out = append(out, Binding{Name: name, Type: kotlinUserTypeText(p, src), Line: nodeLine(p)})
	}
	return out
}

// kotlinReturnType extracts a function's `: T` return-type annotation — the
// `user_type` / `nullable_type` that follows the function_value_parameters
// and precedes the function_body. Only function_declaration carries one;
// secondary constructors have none.
func kotlinReturnType(fn *sitter.Node, src []byte) string {
	if fn.Type() != "function_declaration" {
		return ""
	}
	pastParams := false
	for c := range fn.NamedChildren() {
		switch c.Type() {
		case "function_value_parameters":
			pastParams = true
		case "user_type", "nullable_type":
			if pastParams {
				return strings.TrimSpace(c.Content(src))
			}
		case "function_body":
			return ""
		}
	}
	return ""
}

// kotlinLocalBinding decodes a `val x = <expr>` / `val x: T = <expr>` local
// (or class-body property — the binder routes it into the type scope when
// the property declaration is walked there). Both locals and properties are
// `property_declaration` nodes; the initializer is the named child
// following the variable_declaration.
func kotlinLocalBinding(n *sitter.Node, src []byte) (LocalBind, bool) {
	if n.Type() != "property_declaration" {
		return LocalBind{}, false
	}
	vd := firstChildOfType(n, "variable_declaration")
	if vd == nil {
		return LocalBind{}, false
	}
	name := kotlinSimpleIdent(vd, src)
	if name == "" {
		return LocalBind{}, false
	}
	return LocalBind{
		Name:     name,
		DeclType: kotlinUserTypeText(vd, src),
		Init:     kotlinNamedChildAfter(n, vd),
	}, true
}

// kotlinCall decodes a receiver-qualified call `obj.method()`: a
// call_expression whose callee is a navigation_expression carrying the
// receiver expression and a navigation_suffix that names the method.
// A bare-callee call (`helper()` / `Foo()`) has a simple_identifier callee
// and is not a receiver-qualified call — those are the resolver's job
// (free function) or a construction (NewExprType).
func kotlinCall(n *sitter.Node, src []byte) (*sitter.Node, string, bool) {
	if n.Type() != "call_expression" {
		return nil, "", false
	}
	callee := n.NamedChild(0)
	if callee == nil || callee.Type() != "navigation_expression" {
		return nil, "", false
	}
	recv := callee.NamedChild(0)
	suffix := firstChildOfType(callee, "navigation_suffix")
	if recv == nil || suffix == nil {
		return nil, "", false
	}
	method := kotlinSimpleIdent(suffix, src)
	if method == "" {
		return nil, "", false
	}
	return recv, method, true
}

// kotlinNewExprType returns the constructed type name when n is a Kotlin
// constructor call. Kotlin has no `new`: a `Foo()` call_expression whose
// callee is a Capitalized simple_identifier constructs `Foo`. The
// capitalization gate keeps a lowercase free-function call (`helper()`)
// from being mistaken for a construction — which would otherwise shadow
// the function-return inference path. The apply phase still verifies the
// name against a real graph type node, so a non-type Capitalized callee
// resolves to nothing rather than a false edge.
func kotlinNewExprType(n *sitter.Node, src []byte) string {
	if n.Type() != "call_expression" {
		return ""
	}
	callee := n.NamedChild(0)
	if callee == nil || callee.Type() != "simple_identifier" {
		return ""
	}
	name := strings.TrimSpace(callee.Content(src))
	if !kotlinIsTypeName(name) {
		return ""
	}
	return name
}

// kotlinFieldRef reports that n is a `this.field` access and returns the
// field name. `this.x` is a navigation_expression whose receiver is a
// this_expression and whose navigation_suffix names the field.
func kotlinFieldRef(n *sitter.Node, src []byte) (string, bool) {
	if n.Type() != "navigation_expression" {
		return "", false
	}
	recv := n.NamedChild(0)
	if recv == nil || recv.Type() != "this_expression" {
		return "", false
	}
	suffix := firstChildOfType(n, "navigation_suffix")
	if suffix == nil {
		return "", false
	}
	name := kotlinSimpleIdent(suffix, src)
	if name == "" {
		return "", false
	}
	return name, true
}

// kotlinImports lists the file's `import com.example.Foo` name bindings.
// Local is the bound short name (the trailing segment, or the `as` alias);
// Path is the slash-separated FQN used as the cross-file definition hint.
// Wildcard imports (`import com.example.*`) bind no single name and are
// skipped.
func kotlinImports(root *sitter.Node, src []byte) []Import {
	var out []Import
	var visit func(n *sitter.Node)
	visit = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if n.Type() == "import_header" {
			if imp, ok := kotlinOneImport(n, src); ok {
				out = append(out, imp)
			}
			return
		}
		for c := range n.NamedChildren() {
			visit(c)
		}
	}
	visit(root)
	return out
}

// kotlinOneImport decodes one import_header into a name binding, ok=false
// for a wildcard or nameless import.
func kotlinOneImport(h *sitter.Node, src []byte) (Import, bool) {
	ident := firstChildOfType(h, "identifier")
	if ident == nil {
		return Import{}, false
	}
	fqn := strings.TrimSpace(ident.Content(src))
	if fqn == "" || strings.Contains(h.Content(src), "*") {
		return Import{}, false
	}
	local := fqn
	if i := strings.LastIndex(local, "."); i >= 0 {
		local = local[i+1:]
	}
	// `import com.example.Foo as Bar` renames the local binding.
	if alias := firstChildOfType(h, "import_alias"); alias != nil {
		if a := kotlinTypeName(alias, src); a != "" {
			local = a
		} else if a := kotlinSimpleIdent(alias, src); a != "" {
			local = a
		}
	}
	return Import{Local: local, Path: strings.ReplaceAll(fqn, ".", "/")}, true
}

// normalizeKotlinType reduces a written Kotlin type to the bare name the
// graph indexes: the nullable `?` suffix is stripped, then the shared
// reduction handles generics (`List<Foo>` → `List`) and the dotted package
// qualifier (`com.example.Foo` → `Foo`).
func normalizeKotlinType(t string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return ""
	}
	t = strings.TrimSuffix(t, "?")
	return NormalizeTypeName(t)
}

// kotlinSimpleIdent returns the text of n's first simple_identifier child,
// "" when none — the param / property / field name in these grammars.
func kotlinSimpleIdent(n *sitter.Node, src []byte) string {
	for c := range n.NamedChildren() {
		if c.Type() == "simple_identifier" {
			return c.Content(src)
		}
	}
	return ""
}

// kotlinUserTypeText returns the text of n's first user_type / nullable_type
// child, "" when the binding carries no annotation.
func kotlinUserTypeText(n *sitter.Node, src []byte) string {
	for c := range n.NamedChildren() {
		switch c.Type() {
		case "user_type", "nullable_type":
			return strings.TrimSpace(c.Content(src))
		}
	}
	return ""
}

// kotlinNamedChildAfter returns the named child of parent immediately
// following target, nil when target is last or absent. Used to find a
// property's initializer expression (the named child after its
// variable_declaration; the intervening `=` is anonymous).
func kotlinNamedChildAfter(parent, target *sitter.Node) *sitter.Node {
	found := false
	for c := range parent.NamedChildren() {
		if found {
			return c
		}
		if c.Equal(target) {
			found = true
		}
	}
	return nil
}

// kotlinIsTypeName reports whether name begins with an upper-case letter —
// the Kotlin convention that distinguishes a constructor call (`Foo()`)
// from a free-function call (`helper()`).
func kotlinIsTypeName(name string) bool {
	if name == "" {
		return false
	}
	return unicode.IsUpper([]rune(name)[0])
}
