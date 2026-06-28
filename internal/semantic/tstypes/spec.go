// Package tstypes implements in-process, LSP-free semantic providers
// over the shared tree-sitter ASTs. One shared engine builds a per-file
// scope graph (params, locals, fields, imports), binds declared and
// constructor-inferred types, propagates them through local assignments
// (single-assignment-lite: a rebind to a different type degrades the
// binding to unknown), and resolves receiver-qualified calls plus
// declared supertype relations against the symbol nodes the graph
// already holds. Per-language LangSpec tables adapt the engine to each
// grammar's node vocabulary.
//
// Provenance: everything this package touches is tree-sitter-derived,
// not compiler-verified, so edges are stamped OriginASTResolved (never
// the lsp_* tiers ConfirmEdge uses) with Meta["semantic_source"] set to
// the provider name ("java-types", "python-types", ...). A resolution
// the engine cannot ground in graph evidence — ambiguous receiver,
// unresolvable type name, overloaded method set — is skipped rather
// than guessed: a false edge is worse than a missing one.
package tstypes

import (
	"strings"

	"github.com/zzet/gortex/internal/graph"
	sitter "github.com/zzet/gortex/internal/parser/tsitter"
)

// Binding is one named, optionally typed binding (param or field).
type Binding struct {
	Name string
	Type string // declared type name as written; "" when unannotated
	Line int    // 1-based declaration line
	// Inferred marks a binding whose Type came from an inferential source
	// (a documentation comment, not a checked native annotation). It rides
	// through the type-scope seeding onto the call fact so the apply phase
	// grades a call through such a receiver at the inferred confidence band.
	// Native-annotated bindings leave it false and stay at the direct band.
	Inferred bool
}

// DocTypeKind selects how the binder applies a documentation-comment type
// hint: to a local/field variable, a parameter, or a return type.
type DocTypeKind int

const (
	// DocVar is a `@var T $x` (or bare `@var T` on a property) hint.
	DocVar DocTypeKind = iota
	// DocParam is a `@param T $x` hint.
	DocParam
	// DocReturn is a `@return T` hint (Name is empty).
	DocReturn
)

// DocTypeFact is one type hint recovered from a documentation comment
// (a PHPDoc-style docblock). Kind selects how it applies; Name is the
// variable / parameter name as written in the comment (sigil-included
// where the grammar carries one, e.g. "$x"; empty for a return fact);
// Type is the chosen type name — already reduced to the leftmost
// non-null member of a union and alias-resolved, but NOT yet
// language-normalized (the binder runs spec.normalize on it, which strips
// leading "\", the nullable "?", and generic / array suffixes).
type DocTypeFact struct {
	Kind DocTypeKind
	Name string
	Type string
}

// docParamType returns the `@param` doc-hint type for the parameter named
// name, "" when the docblock carries none.
func docParamType(facts []DocTypeFact, name string) string {
	for _, f := range facts {
		if f.Kind == DocParam && f.Name == name {
			return f.Type
		}
	}
	return ""
}

// docReturnType returns the first `@return` doc-hint type, "" when absent.
func docReturnType(facts []DocTypeFact) string {
	for _, f := range facts {
		if f.Kind == DocReturn {
			return f.Type
		}
	}
	return ""
}

// docVarType returns the `@var` doc-hint type for the variable named name:
// a name-matched `@var T $name` first, else a bare unnamed `@var T`
// fallback (the single-variable form). "" when the docblock carries none.
func docVarType(facts []DocTypeFact, name string) string {
	var unnamed string
	for _, f := range facts {
		if f.Kind != DocVar {
			continue
		}
		if f.Name == name {
			return f.Type
		}
		if f.Name == "" && unnamed == "" {
			unnamed = f.Type
		}
	}
	return unnamed
}

// LocalBind is one local-variable declaration or assignment the engine
// folds into the scope's type environment.
type LocalBind struct {
	Name     string
	DeclType string       // explicit annotation; "" when absent
	Init     *sitter.Node // initializer expression; nil when absent
	Field    bool         // binds in the enclosing type scope (e.g. Ruby @ivar)
}

// SuperRef is one declared supertype relation of a type declaration.
// Kind is EdgeExtends or EdgeImplements when the syntax declares it;
// an empty Kind defers the choice to the apply phase, which picks by
// the resolved target's node kind (used by C#, whose base list does
// not distinguish the base class from interfaces syntactically).
type SuperRef struct {
	Name string
	Kind graph.EdgeKind
	Line int // 1-based
}

// Import is one name-binding import: Local is the identifier the file
// sees; Path is a slash-separated location hint used to prefer the
// matching definition file when several nodes share the name.
type Import struct {
	Local string
	Path  string
}

// AliasRef is one trait-use adaptation that renames an aliased member
// onto the using type (PHP `use T { T::fn as renamed; }`): Alias is the
// new name the using type exposes, Method is the original member name,
// and Trait names the trait the member comes from ("" when the
// adaptation is unqualified, e.g. `use T { fn as renamed; }`).
// Conflict-resolution adaptations (`insteadof`) are deliberately NOT
// represented here — an ambiguous member stays unresolved rather than
// being bound to one arbitrary side.
type AliasRef struct {
	Alias  string
	Trait  string
	Method string
	Line   int
}

// NarrowFact is one type refinement a guard condition imposes on a
// variable. Within the scope the guard governs, Variable provably holds
// type Type. Negated inverts the sense: `!($x instanceof Foo)` yields a
// negated fact — true where the condition is FALSE, i.e. the tail of an
// early-exit guard. The binder applies non-negated facts to the
// then-branch and negated facts to the tail after an early-exit guard;
// it never narrows the else branch.
type NarrowFact struct {
	Variable string // receiver name as written (sigil-included, e.g. "$x")
	Type     string // narrowed type name as written ("" / unresolved is skipped)
	Negated  bool
}

// LangSpec adapts the shared engine to one language's tree-sitter
// grammar. The node-type sets drive the generic walk; the hooks decode
// the handful of shapes that differ per grammar. Hooks may be nil when
// the language has no equivalent construct (e.g. Ruby has no type
// annotations, C# has no name-binding imports).
type LangSpec struct {
	ProviderName string
	Languages    []string

	// GrammarFor returns the grammar for a file path. Per-path because
	// one provider can span sibling grammars (typescript / tsx /
	// javascript).
	GrammarFor func(filePath string) *sitter.Language

	// TypeDeclTypes / FuncDeclTypes are the node types that open a type
	// or callable scope.
	TypeDeclTypes map[string]bool
	FuncDeclTypes map[string]bool

	// SelfName is the receiver keyword ("this", "self"); "" when the
	// language has none.
	SelfName string

	// TypeDeclName extracts the declared type name ("" skips the node).
	TypeDeclName func(n *sitter.Node, src []byte) string

	// Supertypes lists the declared supertype relations of a type decl.
	Supertypes func(n *sitter.Node, src []byte) []SuperRef

	// Fields lists the field bindings of a type decl (declared fields
	// plus whatever conventional initialisations the language grounds,
	// e.g. Python's `self.x = Foo()` or Ruby's `@x = Foo.new`).
	Fields func(n *sitter.Node, src []byte) []Binding

	// Params lists a callable's declared parameters.
	Params func(fn *sitter.Node, src []byte) []Binding

	// ReturnType extracts an explicit return-type annotation ("" when
	// absent or unsupported).
	ReturnType func(fn *sitter.Node, src []byte) string

	// LocalBinding decodes a local declaration / assignment node.
	LocalBinding func(n *sitter.Node, src []byte) (LocalBind, bool)

	// Call decodes a receiver-qualified call: the receiver expression
	// and the method name. ok=false for anything else (including
	// receiverless calls — those are the resolver's job already).
	Call func(n *sitter.Node, src []byte) (recv *sitter.Node, method string, ok bool)

	// NewExprType returns the constructed type name when n is a
	// constructor expression ("" otherwise). Conventional constructors
	// (Python `Foo()`, Ruby `Foo.new`, Rust `Foo::new`) may be
	// returned too — the apply phase verifies every receiver type
	// against a real graph type node before resolving through it.
	NewExprType func(n *sitter.Node, src []byte) string

	// FieldRef reports that n is a reference to an instance field of
	// the current receiver (`this.x`, `self.x`, `@x`) and returns the
	// field's binding name.
	FieldRef func(n *sitter.Node, src []byte) (string, bool)

	// Imports lists the file's name-binding imports.
	Imports func(root *sitter.Node, src []byte) []Import

	// SupertypeKinds widens the node kinds a declared supertype name
	// may resolve to. nil keeps the receiver default (type /
	// interface). Ruby adds packages: tree-sitter modules index as
	// KindPackage and `include M` targets them.
	SupertypeKinds map[graph.NodeKind]bool

	// InheritEdgeKinds lists the edge kinds methodOn climbs when it
	// looks up an inherited member. An empty slice defaults to
	// {EdgeExtends} — only the superclass / supertype chain. Languages
	// whose inheritance spans more than subclassing widen it: Ruby adds
	// EdgeImplements so the modules pulled in by `include` / `prepend`
	// / `extend` contribute their methods. PHP keeps the {EdgeExtends}
	// default: trait composition (`use T;`) is itself modeled as an
	// extends edge, so the default walk already climbs into used traits
	// once they resolve.
	InheritEdgeKinds []graph.EdgeKind

	// ChainedReceivers enables typing a call whose receiver is itself a
	// method call (`a.step().done()`). When set, the binder grounds the
	// inner call's receiver and method, and the apply phase resolves the
	// inner method's declared return type — applying the fluent self /
	// trait return rewrite — to type the outer call's receiver. Off by
	// default; languages with reliable return-type fidelity and fluent
	// chains opt in. The resulting outer edge is graded as inferred.
	ChainedReceivers bool

	// TraitAliases lists the trait-use adaptations that rename an aliased
	// member onto a using type (PHP `use T { T::fn as renamed; }`). nil
	// for languages without the construct. The apply phase routes a call
	// to the alias name through to the original trait member.
	TraitAliases func(n *sitter.Node, src []byte) []AliasRef

	// NormalizeType reduces a written type to the bare name the graph
	// indexes (strip generics / pointers / qualifiers). nil uses the
	// shared default.
	NormalizeType func(t string) string

	// Narrowings decodes a guard / if CONDITION node into zero or more
	// type refinements (PHP `$x instanceof Foo` -> {"$x", "Foo", false};
	// `!($x instanceof Foo)` -> {"$x", "Foo", true}). nil disables
	// narrowing entirely: the binder then descends if-statements through
	// the generic child walk exactly as it descends any other node, so a
	// language that leaves Narrowings (and IfStmt) unset behaves
	// byte-for-byte as before. Consulted only together with IfStmt.
	Narrowings func(cond *sitter.Node, src []byte) []NarrowFact

	// IfStmt decomposes an if-statement node into its condition and its
	// then-body; ok=false for any other node. It is the grammar adapter
	// that lets the shared binder find an if's parts without baking
	// per-grammar field names into the binder. The remaining children of
	// the if (else / else-if clauses) are walked generically without
	// narrowing. Consulted only when Narrowings is set; nil (the default)
	// leaves if-statements to the generic child walk.
	IfStmt func(n *sitter.Node, src []byte) (cond, body *sitter.Node, ok bool)

	// EarlyExit reports whether an if's then-body is a guard clause: a
	// body whose control unconditionally leaves the surrounding flow
	// (return / throw / continue / break). A negated narrowing under such
	// a body refines the variable for the statements that FOLLOW the if in
	// the same scope (the guard's tail). nil disables tail narrowing while
	// still allowing then-branch narrowing. Consulted only when Narrowings
	// and IfStmt are set.
	EarlyExit func(body *sitter.Node, src []byte) bool

	// ExtensionFunctions enables resolving a receiver-qualified call that
	// misses every real member against the language's extension functions —
	// top-level functions declared with a receiver type (`fun Foo.ext()`),
	// which the extractor emits as KindMethod nodes stamped
	// Meta["extension_receiver"]=<receiver type name>. The call phase
	// consults a (receiver, method) index as a FALLBACK, only after a real
	// member lookup (direct + inherited) and any trait-alias miss, so a real
	// member of the same name always wins (members shadow extensions). An
	// extension claimed by more than one declaration on the same receiver
	// stays unresolved rather than guessed. Off by default — a language
	// without extension functions builds no index and behaves exactly as
	// before.
	ExtensionFunctions bool

	// DocType extracts type hints from the documentation comment (docblock)
	// immediately preceding declNode — a parameter-bearing callable, a
	// property, or a local-assignment / statement node. It returns zero or
	// more facts: `@var T $x` / `@var T` (DocVar), `@param T $x` (DocParam),
	// `@return T` (DocReturn). The binder uses each fact as a FALLBACK type
	// source: a native type annotation on the same binding ALWAYS WINS, and
	// DocType supplies a type only where the native annotation is absent. A
	// docblock that cannot be parsed, or whose tag carries no resolvable
	// type, contributes nothing — the binder never guesses from a comment.
	// Because a documentation comment is an author assertion rather than a
	// checked annotation, every edge resolved THROUGH a DocType-supplied
	// type is graded at the inferred confidence band. nil (the default)
	// disables docblock typing entirely, so a language that leaves it unset
	// behaves byte-for-byte as before.
	DocType func(declNode *sitter.Node, src []byte) []DocTypeFact
}

// inheritEdgeKinds returns the edge kinds methodOn climbs when looking
// up an inherited member: the spec's explicit set, or {EdgeExtends}
// when it leaves the field empty — preserving the legacy
// superclass-only walk for every language that does not widen it.
func (s *LangSpec) inheritEdgeKinds() []graph.EdgeKind {
	if len(s.InheritEdgeKinds) > 0 {
		return s.InheritEdgeKinds
	}
	return []graph.EdgeKind{graph.EdgeExtends}
}

func (s *LangSpec) normalize(t string) string {
	if s.NormalizeType != nil {
		return s.NormalizeType(t)
	}
	return NormalizeTypeName(t)
}

// handles reports whether the spec serves the given language code.
func (s *LangSpec) handles(lang string) bool {
	for _, l := range s.Languages {
		if l == lang {
			return true
		}
	}
	return false
}

// NormalizeTypeName is the shared written-type → bare-name reduction:
// strips generic arguments, array suffixes, nullability markers,
// reference sigils, and namespace qualifiers, leaving the identifier
// the graph indexes type nodes under.
func NormalizeTypeName(t string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return ""
	}
	// Reference / pointer / ownership sigils and prefix keywords.
	for {
		switch {
		case strings.HasPrefix(t, "&"), strings.HasPrefix(t, "*"):
			t = strings.TrimSpace(t[1:])
			continue
		case strings.HasPrefix(t, "mut "):
			t = strings.TrimSpace(t[4:])
			continue
		case strings.HasPrefix(t, "dyn "):
			t = strings.TrimSpace(t[4:])
			continue
		case strings.HasPrefix(t, "impl "):
			t = strings.TrimSpace(t[5:])
			continue
		}
		break
	}
	// Generic arguments and array / nullability suffixes.
	if i := strings.IndexAny(t, "<(["); i >= 0 {
		t = t[:i]
	}
	t = strings.TrimSuffix(strings.TrimSuffix(t, "?"), "!")
	// Namespace / module qualifiers — keep the last segment.
	if i := strings.LastIndex(t, "::"); i >= 0 {
		t = t[i+2:]
	}
	if i := strings.LastIndex(t, "."); i >= 0 {
		t = t[i+1:]
	}
	return strings.TrimSpace(t)
}

// nodeLine returns the 1-based start line of n.
func nodeLine(n *sitter.Node) int {
	return int(n.StartPoint().Row) + 1
}

// fieldText returns the text of a named field child, "" when absent.
func fieldText(n *sitter.Node, field string, src []byte) string {
	c := n.ChildByFieldName(field)
	if c == nil {
		return ""
	}
	return c.Content(src)
}

// nameField extracts the `name` field's text — the TypeDeclName shape
// every grammar here shares.
func nameField(n *sitter.Node, src []byte) string {
	return fieldText(n, "name", src)
}

// firstChildOfType returns the first named child with the given type.
func firstChildOfType(n *sitter.Node, t string) *sitter.Node {
	for c := range n.NamedChildren() {
		if c.Type() == t {
			return c
		}
	}
	return nil
}

// identifierLike reports whether the node is a bare single-token name
// usable for scope lookup. "name" is tree-sitter-php's bare identifier
// node — it is the scope of a static `Foo::bar()` call, so it must be
// recognised for the type-qualified receiver path. "simple_identifier"
// is tree-sitter-kotlin's bare identifier — the receiver of a Kotlin
// `dep.bar()` and the right-hand side of a `val x = y` propagation. No
// other registered grammar emits a "name" / "simple_identifier" node in
// receiver / initializer position, so adding them is additive per-grammar.
func identifierLike(t string) bool {
	switch t {
	case "identifier", "constant", "type_identifier", "variable_name", "local_variable", "name", "simple_identifier":
		return true
	}
	return false
}
