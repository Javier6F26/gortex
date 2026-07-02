package resolver

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

// A method-level EdgeOverrides emitted by the extractor for an
// `impl Trait for Type` block resolves to the trait declaration's method
// node — even when the impl-for type is external (io::Error) and thus has
// no local type node.
func TestRustScope_TraitOverrideResolves(t *testing.T) {
	g := buildRustGraph(t, map[string]string{
		"sink.rs": `
pub trait SinkError {
    fn error_message(message: String) -> Self;
}

impl SinkError for io::Error {
    fn error_message(message: String) -> io::Error {
        panic!()
    }
}
`,
	})
	n := ResolveRustScopeCalls(g)
	_ = n

	implID := "sink.rs::io::Error.error_message"
	traitID := "sink.rs::SinkError.error_message"

	found := false
	for _, e := range g.GetOutEdges(implID) {
		if e.Kind == graph.EdgeOverrides && e.To == traitID {
			found = true
		}
	}
	require.True(t, found, "impl override should resolve to the trait method")

	inbound := 0
	for _, e := range g.GetInEdges(traitID) {
		if e.Kind == graph.EdgeOverrides {
			inbound++
		}
	}
	require.Equal(t, 1, inbound, "trait method should have one inbound override")
}
