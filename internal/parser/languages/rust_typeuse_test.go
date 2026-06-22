package languages

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

// TestRustTypeUse_LetBinding asserts a type used only in a `let x: Type`
// binding annotation (never in a param/return) still emits a cross-file
// EdgeTypedAs attributed to the enclosing function.
func TestRustTypeUse_LetBinding(t *testing.T) {
	src := `fn run() {
    let client: HttpClient = make();
    let n: u32 = 0;
}
`
	_, edges := runRustExtract(t, "src/lib.rs", src)

	typed := edgesByKind(edges, graph.EdgeTypedAs)

	var hit *graph.Edge
	for _, e := range typed {
		if e.To == "unresolved::HttpClient" {
			hit = e
		}
	}
	if hit == nil {
		t.Fatalf("expected EdgeTypedAs → unresolved::HttpClient from let binding; got %v", edgeTargets(typed))
	}
	if hit.From != "src/lib.rs::run" {
		t.Errorf("EdgeTypedAs → HttpClient should be owned by enclosing fn src/lib.rs::run; got %q", hit.From)
	}
	if hit.Kind != graph.EdgeTypedAs {
		t.Errorf("expected EdgeTypedAs, got %v", hit.Kind)
	}
	if hit.Origin != graph.OriginASTInferred {
		t.Errorf("expected Origin OriginASTInferred, got %v", hit.Origin)
	}

	// Primitive annotation (`u32`) must not emit a type-use edge.
	for _, e := range typed {
		if e.To == "unresolved::u32" {
			t.Errorf("primitive u32 should not emit EdgeTypedAs; got %v", edgeTargets(typed))
		}
	}
}

// TestRustTypeUse_LetBindingWrappers asserts wrapper / reference types in
// a let annotation canonicalize to the inner named type before the edge
// is emitted (same canonicalization as the param/return emission).
func TestRustTypeUse_LetBindingWrappers(t *testing.T) {
	src := `fn run() {
    let a: Box<Widget> = b();
    let c: &Repo = d();
    let e: Vec<Gadget> = f();
}
`
	_, edges := runRustExtract(t, "src/lib.rs", src)
	typed := edgesByKind(edges, graph.EdgeTypedAs)

	want := map[string]bool{
		"unresolved::Widget": false, // Box<Widget> -> Widget
		"unresolved::Repo":   false, // &Repo -> Repo
		"unresolved::Gadget": false, // Vec<Gadget> -> Gadget
	}
	for _, e := range typed {
		if _, ok := want[e.To]; ok {
			want[e.To] = true
		}
	}
	for tgt, found := range want {
		if !found {
			t.Errorf("expected EdgeTypedAs → %s from let binding; got %v", tgt, edgeTargets(typed))
		}
	}
}

// TestRustTypeUse_GenericArgs asserts that the element types inside a
// generic argument list are captured as `generic_arg` reference edges in
// every position — a let annotation, a parameter, a return type, and a
// nested generic — while type-params and primitives are dropped. The
// annotation / param / return passes only project the head of a
// `generic_type` (and canonicalize loses every arg of a non-wrapper like
// HashMap, plus the error arm of Result), so this is the surface those
// passes leave uncovered.
func TestRustTypeUse_GenericArgs(t *testing.T) {
	src := `fn run<K>(m: HashMap<K, Bar>) -> Result<Baz, MyError> {
    let x: Vec<Foo> = make();
    let y: HashMap<K, Vec<Inner>> = make();
    let n: Vec<u8> = bytes();
    todo!()
}
`
	_, edges := runRustExtract(t, "src/lib.rs", src)

	// let annotation: Vec<Foo> — the element type Foo.
	if !hasRef(t, edges, "Foo", graph.RefContextGenericArg) {
		t.Errorf("let x: Vec<Foo> must emit generic_arg → Foo")
	}
	// parameter: HashMap<K, Bar> — HashMap is not a wrapper, so the head
	// pass loses Bar; the generic_arg pass must recover it.
	if !hasRef(t, edges, "Bar", graph.RefContextGenericArg) {
		t.Errorf("param HashMap<K, Bar> must emit generic_arg → Bar")
	}
	// return type: Result<Baz, MyError> — both arms, including the error
	// arm canonicalize drops.
	if !hasRef(t, edges, "Baz", graph.RefContextGenericArg) {
		t.Errorf("return Result<Baz, MyError> must emit generic_arg → Baz")
	}
	if !hasRef(t, edges, "MyError", graph.RefContextGenericArg) {
		t.Errorf("return Result<Baz, MyError> must emit generic_arg → MyError")
	}
	// nested generic: HashMap<K, Vec<Inner>> — the inner element type.
	if !hasRef(t, edges, "Inner", graph.RefContextGenericArg) {
		t.Errorf("nested HashMap<K, Vec<Inner>> must emit generic_arg → Inner")
	}

	// Negatives: a type-parameter (K) and a primitive arg (u8) must never
	// produce a generic_arg edge.
	for _, e := range edges {
		if e.Kind != graph.EdgeReferences || refEdgeUseKind(e) != graph.RefContextGenericArg {
			continue
		}
		switch e.To {
		case "unresolved::K":
			t.Errorf("type-parameter K must not emit a generic_arg edge")
		case "unresolved::u8":
			t.Errorf("primitive u8 must not emit a generic_arg edge")
		}
	}
}

// TestRustTypeUse_TopLevelLetFallsBackToFile asserts a let binding at the
// crate root (no enclosing function) attributes its type-use edge to the
// file node rather than dropping it.
func TestRustTypeUse_TopLevelLetFallsBackToFile(t *testing.T) {
	// A `let` outside any fn is not valid top-level Rust, but the
	// extractor must still degrade gracefully: a binding the
	// enclosing-fn lookup can't place falls back to the file node ID.
	src := `const _: () = {
    let cfg: AppConfig = load();
};
`
	_, edges := runRustExtract(t, "src/lib.rs", src)
	typed := edgesByKind(edges, graph.EdgeTypedAs)
	for _, e := range typed {
		if e.To == "unresolved::AppConfig" {
			if e.From == "" {
				t.Errorf("EdgeTypedAs → AppConfig must have a non-empty owner")
			}
			return
		}
	}
	t.Errorf("expected EdgeTypedAs → unresolved::AppConfig; got %v", edgeTargets(typed))
}
