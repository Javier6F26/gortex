package languages

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

// edgeKey identifies a reference-form edge by target, kind, and
// ref_context for assertions.
type cppRefEdge struct {
	to         string
	kind       graph.EdgeKind
	refContext string
}

func cppRefEdges(t *testing.T, src string) []cppRefEdge {
	t.Helper()
	res, err := NewCppExtractor().Extract("x.cpp", []byte(src))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	var out []cppRefEdge
	for _, e := range res.Edges {
		rc := ""
		if e.Meta != nil {
			rc, _ = e.Meta["ref_context"].(string)
		}
		out = append(out, cppRefEdge{to: e.To, kind: e.Kind, refContext: rc})
	}
	return out
}

func hasCppRefEdge(edges []cppRefEdge, to string, kind graph.EdgeKind, rc string) bool {
	for _, e := range edges {
		if e.to == to && e.kind == kind && e.refContext == rc {
			return true
		}
	}
	return false
}

func countCppRefEdge(edges []cppRefEdge, to string, kind graph.EdgeKind, rc string) int {
	n := 0
	for _, e := range edges {
		if e.to == to && e.kind == kind && e.refContext == rc {
			n++
		}
	}
	return n
}

func TestCppRefForm_NewInstantiation(t *testing.T) {
	edges := cppRefEdges(t, `void f() { Foo* p = new Foo(1, 2); }`)
	if !hasCppRefEdge(edges, "unresolved::Foo", graph.EdgeInstantiates, "") {
		t.Fatalf("want EdgeInstantiates -> unresolved::Foo; got %+v", edges)
	}
}

func TestCppRefForm_StackConstructionParen(t *testing.T) {
	edges := cppRefEdges(t, `void f() { Bar b(3); }`)
	if !hasCppRefEdge(edges, "unresolved::Bar", graph.EdgeInstantiates, "") {
		t.Fatalf("want EdgeInstantiates -> unresolved::Bar; got %+v", edges)
	}
}

func TestCppRefForm_StackConstructionBrace(t *testing.T) {
	edges := cppRefEdges(t, `void f() { Baz z{4}; }`)
	if !hasCppRefEdge(edges, "unresolved::Baz", graph.EdgeInstantiates, "") {
		t.Fatalf("want EdgeInstantiates -> unresolved::Baz; got %+v", edges)
	}
}

func TestCppRefForm_Inheritance(t *testing.T) {
	edges := cppRefEdges(t, `class X : public Base, private Mixin {};`)
	if !hasCppRefEdge(edges, "unresolved::Base", graph.EdgeReferences, graph.RefContextInherit) {
		t.Fatalf("want inherit -> unresolved::Base; got %+v", edges)
	}
	if !hasCppRefEdge(edges, "unresolved::Mixin", graph.EdgeReferences, graph.RefContextInherit) {
		t.Fatalf("want inherit -> unresolved::Mixin; got %+v", edges)
	}
	// All inherit edges must be OriginASTResolved so the cross-pkg guard
	// doesn't revert them.
	res, err := NewCppExtractor().Extract("x.cpp", []byte(`class X : public Base {};`))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range res.Edges {
		if e.Kind == graph.EdgeReferences && e.To == "unresolved::Base" {
			if e.Origin != graph.OriginASTResolved {
				t.Fatalf("inherit edge origin = %q, want %q", e.Origin, graph.OriginASTResolved)
			}
		}
	}
}

func TestCppRefForm_StructInheritance(t *testing.T) {
	edges := cppRefEdges(t, `struct Derived : Base {};`)
	if !hasCppRefEdge(edges, "unresolved::Base", graph.EdgeReferences, graph.RefContextInherit) {
		t.Fatalf("want struct inherit -> unresolved::Base; got %+v", edges)
	}
}

func TestCppRefForm_StaticCast(t *testing.T) {
	edges := cppRefEdges(t, `void f() { auto a = static_cast<Foo>(x); }`)
	if !hasCppRefEdge(edges, "unresolved::Foo", graph.EdgeReferences, graph.RefContextCast) {
		t.Fatalf("want cast -> unresolved::Foo; got %+v", edges)
	}
}

func TestCppRefForm_DynamicAndReinterpretCast(t *testing.T) {
	edges := cppRefEdges(t, `void f() { auto a = dynamic_cast<Foo*>(x); auto b = reinterpret_cast<Bar>(y); }`)
	if !hasCppRefEdge(edges, "unresolved::Foo", graph.EdgeReferences, graph.RefContextCast) {
		t.Fatalf("want dynamic_cast -> unresolved::Foo; got %+v", edges)
	}
	if !hasCppRefEdge(edges, "unresolved::Bar", graph.EdgeReferences, graph.RefContextCast) {
		t.Fatalf("want reinterpret_cast -> unresolved::Bar; got %+v", edges)
	}
}

func TestCppRefForm_CStyleCast(t *testing.T) {
	edges := cppRefEdges(t, `void f() { auto c = (Quux)x; }`)
	if !hasCppRefEdge(edges, "unresolved::Quux", graph.EdgeReferences, graph.RefContextCast) {
		t.Fatalf("want C-style cast -> unresolved::Quux; got %+v", edges)
	}
}

func TestCppRefForm_StaticMemberAccess(t *testing.T) {
	edges := cppRefEdges(t, `void f() { int n = Color::RED; }`)
	if !hasCppRefEdge(edges, "unresolved::Color", graph.EdgeReferences, graph.RefContextStaticAccess) {
		t.Fatalf("want static_access -> unresolved::Color; got %+v", edges)
	}
}

func TestCppRefForm_StaticMethodCall(t *testing.T) {
	edges := cppRefEdges(t, `void f() { Thing::method(); }`)
	if !hasCppRefEdge(edges, "unresolved::Thing", graph.EdgeReferences, graph.RefContextStaticAccess) {
		t.Fatalf("want static_access -> unresolved::Thing; got %+v", edges)
	}
	// The scope must not be double-emitted (bare qid + call qid).
	if n := countCppRefEdge(edges, "unresolved::Thing", graph.EdgeReferences, graph.RefContextStaticAccess); n != 1 {
		t.Fatalf("static method scope emitted %d times, want 1; got %+v", n, edges)
	}
}

func TestCppRefForm_Negatives(t *testing.T) {
	// Free-function call, plain typed local, std:: helper, primitive cast,
	// and a lowercase scope must emit no reference-form edges.
	cases := []string{
		`void f() { foo(); }`,
		`void f() { int x = 5; }`,
		`void f() { std::move(b); }`,
		`void f() { std::vector<int> v; }`,
		`void f() { int n = detail::flag; }`,
		`void f() { Foo x; }`, // plain declaration, no ctor init -> no instantiate
	}
	for _, src := range cases {
		edges := cppRefEdges(t, src)
		for _, e := range edges {
			if e.kind == graph.EdgeInstantiates ||
				(e.kind == graph.EdgeReferences && (e.refContext == graph.RefContextInherit ||
					e.refContext == graph.RefContextCast || e.refContext == graph.RefContextStaticAccess)) {
				t.Fatalf("src %q: unexpected reference-form edge %+v", src, e)
			}
		}
	}
}

// TestCppRefForm_GenericArgs: a type named inside a template_argument_list
// is a generic_arg reference, in every position — a variable declaration
// (`std::vector<Foo>`), a function parameter (`std::map<std::string, Bar>`),
// and a nested template (`std::map<int, std::vector<Widget>>`). A primitive
// / non-type argument (`int`, the integer constant `5`) emits nothing.
func TestCppRefForm_GenericArgs(t *testing.T) {
	edges := cppRefEdges(t, `void use(std::map<std::string, Bar> m) {
  std::vector<Foo> x;
  std::map<int, std::vector<Widget>> nested;
  std::array<int, 5> a;
}`)
	// Variable-decl template arg.
	if !hasCppRefEdge(edges, "unresolved::Foo", graph.EdgeReferences, graph.RefContextGenericArg) {
		t.Fatalf("want generic_arg -> unresolved::Foo (variable decl); got %+v", edges)
	}
	// Parameter template arg (the non-wrapper map's second argument).
	if !hasCppRefEdge(edges, "unresolved::Bar", graph.EdgeReferences, graph.RefContextGenericArg) {
		t.Fatalf("want generic_arg -> unresolved::Bar (parameter); got %+v", edges)
	}
	// Nested template arg.
	if !hasCppRefEdge(edges, "unresolved::Widget", graph.EdgeReferences, graph.RefContextGenericArg) {
		t.Fatalf("want generic_arg -> unresolved::Widget (nested template); got %+v", edges)
	}
	// Primitives / std aliases / integer constants must not appear as
	// generic_arg references.
	for _, e := range edges {
		if e.refContext != graph.RefContextGenericArg {
			continue
		}
		switch e.to {
		case "unresolved::int", "unresolved::string", "unresolved::5",
			"unresolved::vector", "unresolved::map", "unresolved::array":
			t.Fatalf("false positive: generic_arg edge %+v for a non-user-type argument", e)
		}
	}
	// generic_arg edges must be OriginASTResolved so the cross-pkg guard
	// doesn't revert them.
	res, err := NewCppExtractor().Extract("x.cpp", []byte(`void use() { std::vector<Foo> x; }`))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range res.Edges {
		if e.Kind == graph.EdgeReferences && e.To == "unresolved::Foo" {
			if rc, _ := e.Meta["ref_context"].(string); rc == graph.RefContextGenericArg && e.Origin != graph.OriginASTResolved {
				t.Fatalf("generic_arg edge origin = %q, want %q", e.Origin, graph.OriginASTResolved)
			}
		}
	}
}

func TestCppRefForm_StdQualifiedTypeReducesToTrailing(t *testing.T) {
	// A std::-qualified path whose trailing segment is a Capitalized type
	// reduces to that type; a lowercase trailing one emits nothing.
	edges := cppRefEdges(t, `void f() { int n = std::String::npos; }`)
	if hasCppRefEdge(edges, "unresolved::std", graph.EdgeReferences, graph.RefContextStaticAccess) {
		t.Fatalf("must not emit lowercase std scope; got %+v", edges)
	}
}
