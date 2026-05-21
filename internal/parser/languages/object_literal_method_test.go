package languages

import (
	"strings"
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

// extractFor runs the extractor matching the fixture file's suffix.
func extractFor(t *testing.T, path, src string) *parserExtractResult {
	t.Helper()
	var (
		nodes []*graph.Node
		edges []*graph.Edge
	)
	if strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".tsx") {
		res, err := NewTypeScriptExtractor().Extract(path, []byte(src))
		if err != nil {
			t.Fatalf("ts extract %s: %v", path, err)
		}
		nodes, edges = res.Nodes, res.Edges
	} else {
		res, err := NewJavaScriptExtractor().Extract(path, []byte(src))
		if err != nil {
			t.Fatalf("js extract %s: %v", path, err)
		}
		nodes, edges = res.Nodes, res.Edges
	}
	return &parserExtractResult{nodes: nodes, edges: edges}
}

type parserExtractResult struct {
	nodes []*graph.Node
	edges []*graph.Edge
}

// funcNode returns the function/method node with the given Name, or nil.
func (r *parserExtractResult) funcNode(name string) *graph.Node {
	for _, n := range r.nodes {
		if (n.Kind == graph.KindFunction || n.Kind == graph.KindMethod) && n.Name == name {
			return n
		}
	}
	return nil
}

// callTargetsFrom returns the To-ends of every EdgeCalls leaving from.
func (r *parserExtractResult) callTargetsFrom(from string) []string {
	var out []string
	for _, e := range r.edges {
		if e.Kind == graph.EdgeCalls && e.From == from {
			out = append(out, e.To)
		}
	}
	return out
}

func sliceHas(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// TestObjectLiteralMethodShorthand_Extraction pins the fix for object-
// literal method shorthand — `export const api = { process() {...} }`.
// tree-sitter parses `process()` as a method_definition whose container
// is an `object`, not a class; the old extractor's class-only walk
// dropped it, so the shorthand method had no graph node at all and a
// call `api.process()` could not resolve to it (false negative) — or
// worse, fell through to an unrelated free `process` (false positive).
//
// Both the TS and JS extractors are exercised: the pattern is valid in
// both languages and both used to drop the node.
func TestObjectLiteralMethodShorthand_Extraction(t *testing.T) {
	cases := []struct {
		name string
		path string
		src  string
	}{
		{
			name: "typescript shorthand method",
			path: "svc/api.ts",
			src: `function doStuff(): number { return 1; }
function freeProcess(): number { return 2; }

export const api = {
  process(): number {
    return doStuff();
  },
};

function caller(): void {
  api.process();
}
`,
		},
		{
			name: "javascript shorthand method",
			path: "svc/api.js",
			src: `function doStuff() { return 1; }
function freeProcess() { return 2; }

export const api = {
  process() {
    return doStuff();
  },
};

function caller() {
  api.process();
}
`,
		},
		{
			name: "typescript arrow-field member",
			path: "svc/arrowapi.ts",
			src: `function doStuff(): number { return 1; }

export const api = {
  process: (): number => {
    return doStuff();
  },
};

function caller(): void {
  api.process();
}
`,
		},
		{
			name: "javascript arrow-field member",
			path: "svc/arrowapi.js",
			src: `function doStuff() { return 1; }

export const api = {
  process: () => {
    return doStuff();
  },
};

function caller() {
  api.process();
}
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := extractFor(t, tc.path, tc.src)

			// True positive (false-negative fix): the object-literal
			// member must exist as a function node named `api.process`.
			member := res.funcNode("api.process")
			if member == nil {
				t.Fatalf("object-literal member node %q was not emitted", "api.process")
			}

			// The call inside caller() must bind directly to the member
			// node — already resolved at extraction, never an
			// `unresolved::` placeholder.
			callerID := tc.path + "::caller"
			targets := res.callTargetsFrom(callerID)
			if !sliceHas(targets, member.ID) {
				t.Errorf("api.process() did not bind to the member node %q; call targets = %v", member.ID, targets)
			}

			// False-positive guard: the call must NOT resolve by name to
			// the unrelated free `process` placeholder, and the member
			// node's name must be owner-qualified (not a bare `process`
			// that would collide with a free function of that name).
			if sliceHas(targets, "unresolved::*.process") {
				t.Errorf("api.process() left a bare member placeholder — would mis-resolve to a free `process`")
			}
			if res.funcNode("process") != nil {
				t.Errorf("object-literal member must be owner-qualified, not a bare `process` node")
			}

			// Calls inside the member body must be attributed to the
			// member node, not silently dropped for want of an enclosing
			// function.
			memberCalls := res.callTargetsFrom(member.ID)
			if !sliceHas(memberCalls, "unresolved::doStuff") {
				t.Errorf("call to doStuff() inside the member body was dropped; member calls = %v", memberCalls)
			}
		})
	}
}

// TestObjectLiteralMethod_InlineObjectNotQualified guards the negative
// case: a method shorthand inside an inline / anonymous object (no
// owning `const` binding) still gets a node, but no owner-qualified
// member registration — there is no owner name a call could use.
func TestObjectLiteralMethod_InlineObjectNotQualified(t *testing.T) {
	src := `function use(o: { run(): number }): number { return o.run(); }

use({
  run(): number {
    return 1;
  },
});
`
	res := extractFor(t, "svc/inline.ts", src)
	// The inline shorthand still produces a function node (named by the
	// bare member, since there is no owner) so its body calls attribute.
	if res.funcNode("run") == nil {
		t.Errorf("inline object-literal method should still get a function node")
	}
}
