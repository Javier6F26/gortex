package languages

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

// refEdge finds the first edge with the given kind, target, and
// (optional) ref_context. ref_context == "" matches any (or no) ref_context.
func refEdge(edges []*graph.Edge, kind graph.EdgeKind, to, useKind string) *graph.Edge {
	for _, e := range edges {
		if e.Kind != kind || e.To != to {
			continue
		}
		if useKind == "" {
			return e
		}
		if e.Meta != nil {
			if uk, _ := e.Meta["ref_context"].(string); uk == useKind {
				return e
			}
		}
	}
	return nil
}

// hasRefTo reports whether any edge of any kind targets unresolved::<name>
// with the given ref_context — used by negative assertions.
func hasUseKindTo(edges []*graph.Edge, to, useKind string) bool {
	for _, e := range edges {
		if e.To != to || e.Meta == nil {
			continue
		}
		if uk, _ := e.Meta["ref_context"].(string); uk == useKind {
			return true
		}
	}
	return false
}

// TestJavaRefForm_Instantiation pins `new Foo()` / `new Foo[]` /
// `new Outer.Inner()` → EdgeInstantiates (ref_context=instantiate), and
// the generic arg of `new ArrayList<Request>()` → Request as a
// generic_arg reference (handled by the type_arguments walker, not the
// instantiation case).
func TestJavaRefForm_Instantiation(t *testing.T) {
	src := `package app;
public class Factory {
	void build() {
		Foo f = new Foo();
		Foo[] arr = new Foo[3];
		Outer.Inner oi = new Outer.Inner();
		java.util.List<Request> reqs = new java.util.ArrayList<Request>();
	}
}
`
	_, edges := runJavaExtract(t, "app/Factory.java", src)

	if e := refEdge(edges, graph.EdgeInstantiates, "unresolved::Foo", "instantiate"); e == nil {
		t.Errorf("expected EdgeInstantiates -> Foo (ref_context=instantiate)")
	} else if e.Origin != graph.OriginASTInferred {
		t.Errorf("instantiate Origin = %q, want OriginASTInferred", e.Origin)
	}
	if refEdge(edges, graph.EdgeInstantiates, "unresolved::Inner", "instantiate") == nil {
		t.Errorf("expected EdgeInstantiates -> Inner for `new Outer.Inner()`")
	}
	if refEdge(edges, graph.EdgeInstantiates, "unresolved::ArrayList", "instantiate") == nil {
		t.Errorf("expected EdgeInstantiates -> ArrayList for `new java.util.ArrayList<>()`")
	}
	if refEdge(edges, graph.EdgeReferences, "unresolved::Request", "generic_arg") == nil {
		t.Errorf("expected EdgeReferences -> Request (generic_arg of new ArrayList<Request>())")
	}
}

// TestJavaRefForm_Inheritance pins `extends Foo` / `implements Bar, Baz`
// → EdgeReferences (ref_context=inherit), stamped OriginASTResolved so the
// cross-package guard never reverts them.
func TestJavaRefForm_Inheritance(t *testing.T) {
	src := `package app;
public class Worker extends Base implements Runnable, Closeable {
}
`
	_, edges := runJavaExtract(t, "app/Worker.java", src)

	for _, want := range []string{"Base", "Runnable", "Closeable"} {
		e := refEdge(edges, graph.EdgeReferences, "unresolved::"+want, "inherit")
		if e == nil {
			t.Errorf("expected EdgeReferences -> %s (ref_context=inherit)", want)
			continue
		}
		if e.Origin != graph.OriginASTResolved {
			t.Errorf("inherit -> %s Origin = %q, want OriginASTResolved (else cross_pkg_guard reverts it)", want, e.Origin)
		}
	}
}

// TestJavaRefForm_CastAndInstanceof pins `(Foo) x`, `x instanceof Foo`,
// and the pattern `x instanceof Foo f` → EdgeReferences (ref_context=cast),
// OriginASTResolved.
func TestJavaRefForm_CastAndInstanceof(t *testing.T) {
	src := `package app;
public class C {
	void m(Object o) {
		Foo a = (Foo) o;
		boolean b = o instanceof Bar;
		if (o instanceof Baz z) {}
	}
}
`
	_, edges := runJavaExtract(t, "app/C.java", src)

	for _, want := range []string{"Foo", "Bar", "Baz"} {
		e := refEdge(edges, graph.EdgeReferences, "unresolved::"+want, "cast")
		if e == nil {
			t.Errorf("expected EdgeReferences -> %s (ref_context=cast)", want)
			continue
		}
		if e.Origin != graph.OriginASTResolved {
			t.Errorf("cast -> %s Origin = %q, want OriginASTResolved", want, e.Origin)
		}
	}
}

// TestJavaRefForm_StaticAccess pins `Foo.CONST`, `Foo.class`, and
// `Foo.staticMethod()` whose scope is a Capitalized type → EdgeReferences
// (ref_context=static_access). A `@Foo` annotation also references Foo.
func TestJavaRefForm_StaticAccess(t *testing.T) {
	src := `package app;
@Component
public class C {
	void m() {
		int x = Constants.MAX;
		Class<?> c = Foo.class;
		String s = Helper.format();
	}
}
`
	_, edges := runJavaExtract(t, "app/C.java", src)

	for _, want := range []string{"Constants", "Foo", "Helper"} {
		if refEdge(edges, graph.EdgeReferences, "unresolved::"+want, "static_access") == nil {
			t.Errorf("expected EdgeReferences -> %s (ref_context=static_access)", want)
		}
	}
	// @Component annotation → reference to Component.
	e := refEdge(edges, graph.EdgeReferences, "unresolved::Component", "static_access")
	if e == nil {
		t.Errorf("expected EdgeReferences -> Component for @Component annotation")
	} else if e.Origin != graph.OriginASTResolved {
		t.Errorf("annotation ref Origin = %q, want OriginASTResolved", e.Origin)
	}
}

// TestJavaRefForm_NoFalsePositives pins the capitalization / shadow
// gates: a bare call `bar()`, an instance call `obj.method()`, a
// primitive (`int`), and `this.x` must emit no reference-form edge.
func TestJavaRefForm_NoFalsePositives(t *testing.T) {
	src := `package app;
public class C {
	int field;
	void m(Object obj) {
		bar();
		obj.method();
		int n = 0;
		this.field = n;
		String name = lower.value;
	}
}
`
	_, edges := runJavaExtract(t, "app/C.java", src)

	// No reference-form edge to a lowercase / primitive / this target.
	for _, e := range edges {
		if e.Meta == nil {
			continue
		}
		uk, _ := e.Meta["ref_context"].(string)
		switch uk {
		case "instantiate", "cast", "inherit", "static_access", "generic_arg":
			to := e.To
			// Strip the unresolved:: prefix for the check.
			const p = "unresolved::"
			if len(to) > len(p) && to[:len(p)] == p {
				to = to[len(p):]
			}
			if to == "" {
				continue
			}
			c := to[0]
			if c < 'A' || c > 'Z' {
				t.Errorf("reference-form edge to non-Capitalized target %q (ref_context=%s) — capitalization gate leaked", e.To, uk)
			}
		}
	}

	// Specific negatives.
	if hasUseKindTo(edges, "unresolved::bar", "static_access") || hasUseKindTo(edges, "unresolved::bar", "cast") {
		t.Errorf("bare call bar() emitted a reference-form edge")
	}
	if hasUseKindTo(edges, "unresolved::method", "static_access") {
		t.Errorf("instance call obj.method() emitted a static_access reference")
	}
	if hasUseKindTo(edges, "unresolved::obj", "static_access") {
		t.Errorf("instance receiver obj emitted a static_access reference")
	}
	for _, prim := range []string{"int", "field", "name", "value", "lower"} {
		for _, uk := range []string{"instantiate", "cast", "inherit", "static_access", "generic_arg"} {
			if hasUseKindTo(edges, "unresolved::"+prim, uk) {
				t.Errorf("primitive/lowercase %q emitted a %s reference-form edge", prim, uk)
			}
		}
	}
}

// TestJavaRefForm_GenericArgs pins that the element types of a
// `type_arguments` node are emitted as generic_arg references
// (EdgeReferences, OriginASTResolved) in *every* position, not just the
// expression positions. The declaration-position passes canonicalize the
// annotation to its outer/wrapped type and drop the remaining element
// types, so without the type_arguments walker `Foo` in `Map<String, Foo>`
// is edge-less. This covers field, parameter, return, local, nested, and
// instantiation/cast/inherit positions, and confirms String / wildcards /
// primitives in the arg list contribute nothing.
func TestJavaRefForm_GenericArgs(t *testing.T) {
	src := `package app;
import java.util.List;
import java.util.Map;
import java.util.Optional;
public class Svc<T extends Bound> extends Base<Sup> implements Sink<Iface> {
	private Map<String, FieldT> fieldMap;
	private Cache<FieldU> fieldCache;
	public Optional<RetT> getOpt(List<ParamT> items, Map<String, List<NestT>> nested) {
		Map<String, LocalT> local = build();
		Repository<WidgetT> repo = make();
		List<NewT> made = new java.util.ArrayList<NewT>();
		Object o = (Holder<CastT>) made;
		boolean b = o instanceof Box<TestT>;
		List<? extends WildLow> wild = wild();
		List<Integer> nums = null;
		return null;
	}
}
`
	_, edges := runJavaExtract(t, "app/Svc.java", src)

	// Every element type, regardless of position, must surface as a
	// generic_arg EdgeReferences (OriginASTResolved).
	wantArgs := []string{
		"FieldT",  // field Map<String, FieldT>
		"FieldU",  // field Cache<FieldU>
		"RetT",    // return Optional<RetT>
		"ParamT",  // param List<ParamT>
		"NestT",   // nested param Map<String, List<NestT>>
		"LocalT",  // local Map<String, LocalT>
		"WidgetT", // local Repository<WidgetT>
		"NewT",    // new ArrayList<NewT>()
		"CastT",   // (Holder<CastT>) cast
		"TestT",   // instanceof Box<TestT>
		"Sup",     // extends Base<Sup>
		"Iface",   // implements Sink<Iface>
	}
	for _, want := range wantArgs {
		e := refEdge(edges, graph.EdgeReferences, "unresolved::"+want, "generic_arg")
		if e == nil {
			t.Errorf("expected EdgeReferences -> %s (ref_context=generic_arg)", want)
			continue
		}
		if e.Origin != graph.OriginASTResolved {
			t.Errorf("generic_arg -> %s Origin = %q, want OriginASTResolved", want, e.Origin)
		}
	}

	// Wildcard bound type (`? extends WildLow`) is also an element mention.
	if refEdge(edges, graph.EdgeReferences, "unresolved::WildLow", "generic_arg") == nil {
		t.Errorf("expected EdgeReferences -> WildLow (generic_arg of `? extends WildLow`)")
	}

	// `String` is treated as a primitive by isJavaPrimitive (same gate the
	// declaration-position passes use), so a `Map<String, …>` key must NOT
	// produce a generic_arg reference.
	if hasUseKindTo(edges, "unresolved::String", "generic_arg") {
		t.Errorf("String must not be emitted as a generic_arg reference (primitive gate)")
	}
	// A boxed type that is *not* on the primitive list (Integer) is a real
	// type reference and must surface, confirming the gate is the same one
	// the type-position edges use rather than an over-broad drop.
	if refEdge(edges, graph.EdgeReferences, "unresolved::Integer", "generic_arg") == nil {
		t.Errorf("expected EdgeReferences -> Integer (generic_arg of List<Integer>)")
	}
}
