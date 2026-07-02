package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

// Trait-declaration methods (both signatures and default methods) get their
// own <file>::<Trait>.<method> KindMethod node so dyn-dispatch call sites that
// bind to the declaration are answerable and find_implementations can pair the
// concrete impls against them.
func TestRsExtractor_TraitMethodNodes(t *testing.T) {
	src := []byte(`pub trait SinkError: Sized {
    fn error_message<T>(message: T) -> Self;

    fn default_impl(&self) -> bool {
        true
    }
}
`)
	e := NewRustExtractor()
	result, err := e.Extract("sink.rs", src)
	require.NoError(t, err)

	em := nodeByID(result.Nodes, "sink.rs::SinkError.error_message")
	require.NotNil(t, em, "signature method node")
	assert.Equal(t, graph.KindMethod, em.Kind)
	assert.Equal(t, "SinkError", em.Meta["receiver"])
	assert.Equal(t, "true", em.Meta["trait_decl"])

	di := nodeByID(result.Nodes, "sink.rs::SinkError.default_impl")
	require.NotNil(t, di, "default method node")
	assert.Equal(t, graph.KindMethod, di.Kind)
	assert.Equal(t, "true", di.Meta["trait_decl"])

	// The default method must NOT leak as a bare free function.
	assert.Nil(t, nodeByID(result.Nodes, "sink.rs::default_impl"))

	// Each trait method is a member of the trait interface node.
	members := 0
	for _, ed := range result.Edges {
		if ed.Kind == graph.EdgeMemberOf && ed.To == "sink.rs::SinkError" &&
			(ed.From == "sink.rs::SinkError.error_message" || ed.From == "sink.rs::SinkError.default_impl") {
			members++
		}
	}
	assert.Equal(t, 2, members)

	// Backward compat: the interface node still lists method names in Meta.
	iface := nodeByID(result.Nodes, "sink.rs::SinkError")
	require.NotNil(t, iface)
	names, _ := iface.Meta["methods"].([]string)
	assert.Contains(t, names, "error_message")
}

// `impl Trait for Type` methods emit a method-level EdgeOverrides at the impl
// method, even when the implemented-for type is external (io::Error) and thus
// has no local type node — the case inferOverrides cannot cover.
func TestRsExtractor_TraitImplOverrides(t *testing.T) {
	src := []byte(`pub trait SinkError: Sized {
    fn error_message<T>(message: T) -> Self;
}

impl SinkError for io::Error {
    fn error_message<T>(message: T) -> io::Error {
        io::Error::new(io::ErrorKind::Other, message.to_string())
    }
}
`)
	e := NewRustExtractor()
	result, err := e.Extract("sink.rs", src)
	require.NoError(t, err)

	require.NotNil(t, nodeByID(result.Nodes, "sink.rs::io::Error.error_message"))

	var ov *graph.Edge
	for _, ed := range result.Edges {
		if ed.Kind == graph.EdgeOverrides && ed.From == "sink.rs::io::Error.error_message" {
			ov = ed
			break
		}
	}
	require.NotNil(t, ov, "impl method should emit an EdgeOverrides")
	assert.Equal(t, "unresolved::SinkError.error_message", ov.To)
}

// An inherent impl (no trait) must not emit any override edge.
func TestRsExtractor_InherentImplNoOverride(t *testing.T) {
	src := []byte(`struct Foo {}
impl Foo {
    fn bar(&self) {}
}
`)
	e := NewRustExtractor()
	result, err := e.Extract("foo.rs", src)
	require.NoError(t, err)
	for _, ed := range result.Edges {
		assert.NotEqual(t, graph.EdgeOverrides, ed.Kind, "inherent impl should not override")
	}
}
