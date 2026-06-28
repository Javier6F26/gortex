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
		// A method call standing in receiver position (`p.copy().component1()`,
		// `a.step().done()`) types its receiver from the inner call's
		// (rewritten) return type, so a fluent chain resolves link by link.
		// The chain path grounds only when every link lands on a real member
		// with a known return type — a missing link beats a wrong edge.
		ChainedReceivers: true,
		// Kotlin operators are sugar for named member functions (`a + b` is
		// `a.plus(b)`, `a[i]` is `a.get(i)`, `a in b` is `b.contains(a)`).
		// This desugars an operator expression into the member call it
		// denotes, so an operator on a user type that declares the operator
		// function resolves to that function — while an operator on a
		// primitive (`1 + 2`) resolves to nothing.
		SyntheticCalls: kotlinSyntheticCalls,
		// Smart-cast narrowing: an `is` check in an `if` guard or a `when`
		// arm refines the subject's type in the guarded scope, so a call on
		// the narrowed type resolves. The `if` cases reuse the shared
		// narrowing binder via Narrowings + IfStmt + EarlyExit; the `when`
		// case is decoded by SubjectNarrowings.
		Narrowings:        kotlinNarrowings,
		IfStmt:            kotlinIfStmt,
		EarlyExit:         kotlinEarlyExit,
		SubjectNarrowings: kotlinSubjectNarrowings,
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

// kotlinBinaryOps maps each overloadable binary operator token to the member
// function it desugars to. The receiver is the left operand for all of these.
var kotlinBinaryOps = map[string]string{
	"+":  "plus",
	"-":  "minus",
	"*":  "times",
	"/":  "div",
	"%":  "rem",
	"..": "rangeTo",
}

// kotlinSyntheticCalls desugars a Kotlin operator / sugar expression into the
// member call it denotes, so the binder can resolve `a + b` to a user type's
// `operator fun plus`. It reads the GRAMMAR operator node (the unnamed token
// child) rather than scanning the source text, and switches on the
// expression node kind:
//
//   - additive / multiplicative / range:  a OP b  -> a.plus|minus|times|div|rem|rangeTo(b)
//   - comparison (< > <= >=):              a OP b  -> a.compareTo(b)
//   - subscript get:                       a[i]    -> a.get(i)
//   - subscript set:                       a[i]=v  -> a.set(i, v)
//   - membership (in / !in):               a in b  -> b.contains(a)   (receiver is the RHS)
//   - increment / decrement (++ / --):     a++/--a -> a.inc() / a.dec()
//   - for-loop:                            for (x in coll) -> coll.iterator()
//
// `is` / `!is` type checks (which share check_expression with `in`) and the
// unary `-` / `+` / `!` prefixes (which share prefix_expression with `++` /
// `--`) are deliberately NOT desugared — they are not the member-call sugar
// this resolves. Anything else returns nil.
func kotlinSyntheticCalls(n *sitter.Node, src []byte) []SyntheticCall {
	switch n.Type() {
	case "additive_expression", "multiplicative_expression", "range_expression":
		method, ok := kotlinBinaryOps[kotlinOperatorToken(n, src)]
		if !ok {
			return nil
		}
		return kotlinBinaryCall(n, method)
	case "comparison_expression":
		// All four relational operators (< > <= >=) desugar to compareTo;
		// equality (== !=) is a separate node and is not desugared here.
		return kotlinBinaryCall(n, "compareTo")
	case "indexing_expression":
		recv := n.NamedChild(0)
		if recv == nil {
			return nil
		}
		return []SyntheticCall{{Receiver: recv, Method: "get", Args: kotlinIndexArgs(n)}}
	case "assignment":
		return kotlinIndexSet(n, src)
	case "check_expression":
		return kotlinMembershipCall(n, src)
	case "prefix_expression", "postfix_expression":
		return kotlinIncDecCall(n, src)
	case "for_statement":
		recv := kotlinForCollection(n)
		if recv == nil {
			return nil
		}
		return []SyntheticCall{{Receiver: recv, Method: "iterator"}}
	}
	return nil
}

// kotlinBinaryCall builds the desugared call for a two-operand operator whose
// receiver is the left operand and whose single argument is the right operand.
func kotlinBinaryCall(n *sitter.Node, method string) []SyntheticCall {
	lhs := n.NamedChild(0)
	if lhs == nil {
		return nil
	}
	sc := SyntheticCall{Receiver: lhs, Method: method}
	if rhs := kotlinLastNamedChild(n); rhs != nil && !rhs.Equal(lhs) {
		sc.Args = []*sitter.Node{rhs}
	}
	return []SyntheticCall{sc}
}

// kotlinMembershipCall desugars a membership test. `a in b` and `a !in b`
// share the check_expression node with `a is T` / `a !is T`; only the `in`
// forms desugar — to `b.contains(a)`, whose RECEIVER is the right operand
// (the collection), not the left. A type check (`is`) is skipped: it is not
// a member-call desugaring.
func kotlinMembershipCall(n *sitter.Node, src []byte) []SyntheticCall {
	isIn := false
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil || c.IsNamed() {
			continue
		}
		switch c.Content(src) {
		case "in":
			isIn = true
		case "is":
			return nil
		}
	}
	if !isIn {
		return nil
	}
	lhs := n.NamedChild(0)
	rhs := kotlinLastNamedChild(n)
	if rhs == nil {
		return nil
	}
	sc := SyntheticCall{Receiver: rhs, Method: "contains"}
	if lhs != nil && !lhs.Equal(rhs) {
		sc.Args = []*sitter.Node{lhs}
	}
	return []SyntheticCall{sc}
}

// kotlinIncDecCall desugars `a++` / `++a` to `a.inc()` and `a--` / `--a` to
// `a.dec()`. The non-null assertion (`a!!`, which shares postfix_expression)
// and the unary `-` / `+` / `!` prefixes are not desugared.
func kotlinIncDecCall(n *sitter.Node, src []byte) []SyntheticCall {
	var method string
	switch kotlinOperatorToken(n, src) {
	case "++":
		method = "inc"
	case "--":
		method = "dec"
	default:
		return nil
	}
	recv := n.NamedChild(0)
	if recv == nil {
		return nil
	}
	return []SyntheticCall{{Receiver: recv, Method: method}}
}

// kotlinIndexSet desugars `a[i] = v` to `a.set(i, v)`. It fires only on a
// plain `=` assignment whose target is a directly_assignable_expression
// carrying an indexing_suffix — a plain `a = v` (no subscript) or an
// augmented `a[i] += v` (operator token not `=`) is left alone.
func kotlinIndexSet(n *sitter.Node, src []byte) []SyntheticCall {
	if kotlinOperatorToken(n, src) != "=" {
		return nil
	}
	target := n.NamedChild(0)
	if target == nil || target.Type() != "directly_assignable_expression" {
		return nil
	}
	suffix := firstChildOfType(target, "indexing_suffix")
	if suffix == nil {
		return nil
	}
	var recv *sitter.Node
	for c := range target.NamedChildren() {
		if c.Type() == "indexing_suffix" {
			continue
		}
		recv = c
		break
	}
	if recv == nil {
		return nil
	}
	args := kotlinIndexArgs(target)
	if rhs := kotlinLastNamedChild(n); rhs != nil && !rhs.Equal(target) {
		args = append(args, rhs)
	}
	return []SyntheticCall{{Receiver: recv, Method: "set", Args: args}}
}

// kotlinIndexArgs returns the index expression nodes of an indexing_suffix
// child of n (the `i` in `a[i]`), nil when there is none.
func kotlinIndexArgs(n *sitter.Node) []*sitter.Node {
	suffix := firstChildOfType(n, "indexing_suffix")
	if suffix == nil {
		return nil
	}
	var args []*sitter.Node
	for c := range suffix.NamedChildren() {
		args = append(args, c)
	}
	return args
}

// kotlinForCollection returns the iterated collection expression of a
// for_statement: the named child that is neither the loop variable
// declaration nor the loop body.
func kotlinForCollection(n *sitter.Node) *sitter.Node {
	for c := range n.NamedChildren() {
		switch c.Type() {
		case "variable_declaration", "multi_variable_declaration", "control_structure_body":
			continue
		default:
			return c
		}
	}
	return nil
}

// kotlinOperatorToken returns the text of n's operator token — its first
// unnamed (anonymous) child, whose content is the literal operator (`+`,
// `..`, `++`, `in` …). "" when n has no anonymous child.
func kotlinOperatorToken(n *sitter.Node, src []byte) string {
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c != nil && !c.IsNamed() {
			return c.Content(src)
		}
	}
	return ""
}

// kotlinLastNamedChild returns n's last named child, nil when n has none.
func kotlinLastNamedChild(n *sitter.Node) *sitter.Node {
	cnt := int(n.NamedChildCount())
	if cnt == 0 {
		return nil
	}
	return n.NamedChild(cnt - 1)
}

// kotlinIfStmt decomposes a Kotlin if into its condition and then-branch.
// tree-sitter-kotlin models `if` as an if_expression (it is an expression,
// `val y = if (c) a else b`) carrying a `condition` field and a
// `consequence` field (the then control_structure_body); the `else` branch
// is the `alternative` field, left for the binder to walk generically
// without narrowing. ok=false for any non-if node.
func kotlinIfStmt(n *sitter.Node, _ []byte) (cond, body *sitter.Node, ok bool) {
	if n.Type() != "if_expression" {
		return nil, nil, false
	}
	return n.ChildByFieldName("condition"), n.ChildByFieldName("consequence"), true
}

// kotlinNarrowings decodes an if condition into type refinements. It
// recognises the smart-cast type check `x is Foo` — a check_expression
// whose left operand is a bare variable (simple_identifier) and whose
// `is` operator token is followed by the checked type — and its negation
// `x !is Foo`, marked by a leading `!` token. `x is Foo` yields
// {"x", "Foo", false}; `x !is Foo` yields {"x", "Foo", true} (true where
// the check is FALSE — the tail of an early-exit guard). The membership
// test `x in coll` shares check_expression but carries no `is` token and
// is left alone (it is a collection call, desugared by SyntheticCalls, not
// a type refinement). A null check (`x != null`) is NOT emitted as a fact:
// a nullable annotation is already reduced to its non-null base by
// normalizeKotlinType at bind time, so the variable resolves on its base
// type inside the guard without an explicit narrowing — emitting one would
// only re-bind the same type at a weaker band. A property / call / indexed
// receiver is left unresolved — precision over recall.
func kotlinNarrowings(cond *sitter.Node, src []byte) []NarrowFact {
	if cond == nil || cond.Type() != "check_expression" {
		return nil
	}
	isCheck, negated := false, false
	for i := 0; i < int(cond.ChildCount()); i++ {
		c := cond.Child(i)
		if c == nil || c.IsNamed() {
			continue
		}
		switch c.Content(src) {
		case "is":
			isCheck = true
		case "!":
			negated = true
		}
	}
	if !isCheck {
		return nil
	}
	lhs := cond.NamedChild(0)
	if lhs == nil || lhs.Type() != "simple_identifier" {
		return nil
	}
	typeNode := kotlinCheckType(cond)
	if typeNode == nil {
		return nil
	}
	variable := strings.TrimSpace(lhs.Content(src))
	typ := strings.TrimSpace(typeNode.Content(src))
	if variable == "" || typ == "" {
		return nil
	}
	return []NarrowFact{{Variable: variable, Type: typ, Negated: negated}}
}

// kotlinCheckType returns the type node of a check_expression (`x is Foo`):
// its user_type / nullable_type named child, nil when absent.
func kotlinCheckType(cond *sitter.Node) *sitter.Node {
	var typeNode *sitter.Node
	for c := range cond.NamedChildren() {
		switch c.Type() {
		case "user_type", "nullable_type":
			typeNode = c
		}
	}
	return typeNode
}

// kotlinEarlyExit reports whether an if then-branch is a guard clause whose
// control unconditionally leaves the surrounding flow. The body is the
// `consequence` control_structure_body: a brace-less `if (…) return` holds
// the jump_expression directly, while a braced `if (…) { … ; return }`
// wraps its statements in a `statements` node, whose LAST statement decides
// (any preceding statements still run, then control leaves). Kotlin folds
// return / break / continue / throw all into a single jump_expression node.
func kotlinEarlyExit(body *sitter.Node, _ []byte) bool {
	if body == nil {
		return false
	}
	last := body
	if stmts := firstChildOfType(body, "statements"); stmts != nil {
		last = kotlinLastNamedChild(stmts)
	} else if body.Type() == "control_structure_body" {
		last = kotlinLastNamedChild(body)
	}
	return last != nil && last.Type() == "jump_expression"
}

// kotlinSubjectNarrowings decodes a `when (x) { is Foo -> … }` into a
// SubjectMatch. The subject is narrowable only when it is a bare variable
// (`when (x)`, a simple_identifier) — a `when (val y = …)` declaration or a
// computed subject names nothing to refine. An arm narrows the subject only
// when it carries exactly one `is Type` pattern (a multi-pattern
// `is A, is B ->` arm is ambiguous and narrows nothing; an `else` arm has
// no pattern). Every arm's body is returned so the binder walks it — under
// the narrowed scope when facts are present, unrefined otherwise. ok=false
// for any non-when node.
func kotlinSubjectNarrowings(n *sitter.Node, src []byte) (SubjectMatch, bool) {
	if n.Type() != "when_expression" {
		return SubjectMatch{}, false
	}
	subj := firstChildOfType(n, "when_subject")
	subjVar := ""
	if subj != nil {
		subjVar = kotlinSimpleIdent(subj, src)
	}
	var branches []SubjectBranch
	for c := range n.NamedChildren() {
		if c.Type() != "when_entry" {
			continue
		}
		var conds []*sitter.Node
		for cc := range c.NamedChildren() {
			if cc.Type() == "when_condition" {
				conds = append(conds, cc)
			}
		}
		var facts []NarrowFact
		if subjVar != "" && len(conds) == 1 {
			if typ := kotlinTypeTestName(conds[0], src); typ != "" {
				facts = []NarrowFact{{Variable: subjVar, Type: typ, Negated: false}}
			}
		}
		branches = append(branches, SubjectBranch{
			Conds: conds,
			Body:  firstChildOfType(c, "control_structure_body"),
			Facts: facts,
		})
	}
	return SubjectMatch{Subject: subj, Branches: branches}, true
}

// kotlinTypeTestName returns the type named by a when_condition's positive
// `is Type` test, "" otherwise. A negated `!is Type` test narrows the
// complement, not the type, so it is skipped (the leading `!` token).
func kotlinTypeTestName(cond *sitter.Node, src []byte) string {
	tt := firstChildOfType(cond, "type_test")
	if tt == nil {
		return ""
	}
	for i := 0; i < int(tt.ChildCount()); i++ {
		c := tt.Child(i)
		if c != nil && !c.IsNamed() && c.Content(src) == "!" {
			return ""
		}
	}
	for c := range tt.NamedChildren() {
		switch c.Type() {
		case "user_type", "nullable_type":
			return strings.TrimSpace(c.Content(src))
		}
	}
	return ""
}
