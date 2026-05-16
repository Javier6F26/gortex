package languages

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

// Helper: return all EdgeThrows targets in the result, deduped.
func tsThrowsTargets(edges []*graph.Edge) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range edges {
		if e.Kind != graph.EdgeThrows {
			continue
		}
		if seen[e.To] {
			continue
		}
		seen[e.To] = true
		out = append(out, e.To)
	}
	return out
}

func TestTSThrows_NewExpression(t *testing.T) {
	src := `function validate(x: number): void {
  if (x < 0) {
    throw new RangeError("negative");
  }
}
`
	_, edges := runTSExtract(t, "src/v.ts", src)
	targets := tsThrowsTargets(edges)
	want := []string{"unresolved::RangeError"}
	if len(targets) != 1 || targets[0] != want[0] {
		t.Errorf("got %v, want %v", targets, want)
	}
	// Origin should be ASTInferred (TS doesn't enforce checked exceptions).
	for _, e := range edges {
		if e.Kind == graph.EdgeThrows && e.Origin != graph.OriginASTInferred {
			t.Errorf("EdgeThrows origin = %q, want ast_inferred", e.Origin)
		}
	}
}

func TestTSThrows_BareIdentifier(t *testing.T) {
	src := `function rethrow(err: Error): never {
  throw err;
}
`
	_, edges := runTSExtract(t, "src/r.ts", src)
	targets := tsThrowsTargets(edges)
	if len(targets) != 1 || targets[0] != "unresolved::err" {
		t.Errorf("got %v, want [unresolved::err]", targets)
	}
}

func TestTSThrows_NamespacedConstructor(t *testing.T) {
	src := `function load(): void {
  throw new errors.NotFoundError("missing");
}
`
	_, edges := runTSExtract(t, "src/n.ts", src)
	targets := tsThrowsTargets(edges)
	if len(targets) != 1 || targets[0] != "unresolved::NotFoundError" {
		t.Errorf("got %v, want [unresolved::NotFoundError]", targets)
	}
}

func TestTSThrows_MemberExpression(t *testing.T) {
	src := `function fail(kind: string): void {
  throw errors.kinds.MyError;
}
`
	_, edges := runTSExtract(t, "src/m.ts", src)
	targets := tsThrowsTargets(edges)
	if len(targets) != 1 || targets[0] != "unresolved::MyError" {
		t.Errorf("got %v, want [unresolved::MyError]", targets)
	}
}

func TestTSThrows_StringLiteralSkipped(t *testing.T) {
	src := `function bad(): void {
  throw "oops";
  throw 42;
}
`
	_, edges := runTSExtract(t, "src/s.ts", src)
	targets := tsThrowsTargets(edges)
	if len(targets) != 0 {
		t.Errorf("string/numeric throws should NOT emit edges; got %v", targets)
	}
}

func TestTSThrows_DedupesPerFunction(t *testing.T) {
	src := `function many(): void {
  if (1) throw new MyError("a");
  if (2) throw new MyError("b");
  if (3) throw new MyError("c");
}
`
	_, edges := runTSExtract(t, "src/d.ts", src)
	targets := tsThrowsTargets(edges)
	if len(targets) != 1 {
		t.Errorf("expected one deduped target, got %d (%v)", len(targets), targets)
	}
}

func TestTSThrows_MultipleDistinctTypes(t *testing.T) {
	src := `function many(): void {
  throw new TypeError("a");
  throw new RangeError("b");
  throw new MyCustomError("c");
}
`
	_, edges := runTSExtract(t, "src/multi.ts", src)
	targets := tsThrowsTargets(edges)
	if len(targets) != 3 {
		t.Errorf("expected 3 distinct targets, got %d (%v)", len(targets), targets)
	}
}

// Nested function bodies should NOT contribute their throws to the
// outer function — they're emitted against the nested function's own
// ID via its own emitFunction call.
func TestTSThrows_SkipsNestedFunctionScope(t *testing.T) {
	src := `function outer(): void {
  throw new OuterError("a");
  function inner(): void {
    throw new InnerError("b");
  }
}
`
	_, edges := runTSExtract(t, "src/nest.ts", src)
	// outer should see only OuterError; inner should see only InnerError.
	outerEdges := []string{}
	innerEdges := []string{}
	for _, e := range edges {
		if e.Kind != graph.EdgeThrows {
			continue
		}
		switch e.From {
		case "src/nest.ts::outer":
			outerEdges = append(outerEdges, e.To)
		case "src/nest.ts::inner":
			innerEdges = append(innerEdges, e.To)
		}
	}
	if len(outerEdges) != 1 || outerEdges[0] != "unresolved::OuterError" {
		t.Errorf("outer throws = %v, want [unresolved::OuterError]", outerEdges)
	}
	if len(innerEdges) != 1 || innerEdges[0] != "unresolved::InnerError" {
		t.Errorf("inner throws = %v, want [unresolved::InnerError]", innerEdges)
	}
}

// Arrow-function bodies inside a method don't leak their throws to
// the enclosing method.
func TestTSThrows_SkipsArrowFunctionScope(t *testing.T) {
	src := `class S {
  handle(): void {
    throw new HandleError("a");
    const cb = (): void => { throw new CbError("b"); };
    cb();
  }
}
`
	_, edges := runTSExtract(t, "src/cls.ts", src)
	for _, e := range edges {
		if e.Kind != graph.EdgeThrows {
			continue
		}
		// The method's throws should NOT include CbError.
		if e.From == "src/cls.ts::S.handle" && e.To == "unresolved::CbError" {
			t.Errorf("method.handle should NOT see arrow's CbError")
		}
	}
}
