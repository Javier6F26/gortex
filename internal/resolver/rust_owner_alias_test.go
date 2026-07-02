package resolver

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A `Type::method()` call resolves to a method whose impl type carries
// generic/lifetime args (Candidate<'a>), via the base-name owner alias.
func TestRustScope_GenericOwnerAlias(t *testing.T) {
	g := buildRustGraph(t, map[string]string{
		"lib.rs": `
struct Candidate<'a> { x: &'a str }

impl<'a> Candidate<'a> {
    fn new(s: &'a str) -> Candidate<'a> { Candidate { x: s } }
}

fn make() {
    let _c = Candidate::new("x");
}
`,
	})
	ResolveRustScopeCalls(g)
	targets := callTargetsFromRust(g, "lib.rs::make")
	require.Contains(t, targets, "lib.rs::Candidate<'a>.new")
}
