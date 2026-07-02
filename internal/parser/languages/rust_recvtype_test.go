package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

func recvTypeOfCall(edges []*graph.Edge, from string) string {
	for _, ed := range edges {
		if ed.Kind == graph.EdgeCalls && ed.From == from {
			if rt, _ := ed.Meta["receiver_type"].(string); rt != "" {
				return rt
			}
		}
	}
	return ""
}

// A selector call on a parameter receiver stamps receiver_type from the
// caller's own parameter scope (mirrors Go's paramsByFunc), so the resolver
// can bind it to the right method.
func TestRsExtractor_ParamReceiverType(t *testing.T) {
	src := []byte(`struct HiArgs {}
impl HiArgs { fn has_implicit_path(&self) -> bool { true } }
fn search(args: &HiArgs) -> bool { args.has_implicit_path() }
`)
	e := NewRustExtractor()
	result, err := e.Extract("main.rs", src)
	require.NoError(t, err)
	assert.Equal(t, "HiArgs", recvTypeOfCall(result.Edges, "main.rs::search"))
}

// A self selector call binds to the enclosing impl type via the self scope.
func TestRsExtractor_SelfReceiverType(t *testing.T) {
	src := []byte(`struct Foo {}
impl Foo {
    fn a(&self) { self.b() }
    fn b(&self) {}
}
`)
	e := NewRustExtractor()
	result, err := e.Extract("foo.rs", src)
	require.NoError(t, err)
	assert.Equal(t, "Foo", recvTypeOfCall(result.Edges, "foo.rs::Foo.a"))
}

// A parameter shadows a same-named file-wide let binding at call resolution.
func TestRsExtractor_ParamShadowsLet(t *testing.T) {
	src := []byte(`struct A {}
struct B {}
impl A { fn go(&self) {} }
impl B { fn go(&self) {} }
fn outer(x: &A) { x.go(); }
fn other() { let x = B {}; x.go(); }
`)
	e := NewRustExtractor()
	result, err := e.Extract("m.rs", src)
	require.NoError(t, err)
	assert.Equal(t, "A", recvTypeOfCall(result.Edges, "m.rs::outer"))
	assert.Equal(t, "B", recvTypeOfCall(result.Edges, "m.rs::other"))
}
