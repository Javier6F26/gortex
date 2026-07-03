package resolver

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A bare `RegexMatcherBuilder::new()` call is ambiguous when two crates
// define that type, but a same-crate call almost always means the same-crate
// type. The scope pass breaks the tie by the caller's crate (the path up to
// "/src/").
func TestRustScope_SameCrateDisambiguation(t *testing.T) {
	g := buildRustGraph(t, map[string]string{
		"crates/regex/src/matcher.rs": `
struct RegexMatcherBuilder {}
impl RegexMatcherBuilder {
    fn new() -> RegexMatcherBuilder { RegexMatcherBuilder {} }
}
fn make() {
    let _b = RegexMatcherBuilder::new();
}
`,
		"crates/pcre2/src/matcher.rs": `
struct RegexMatcherBuilder {}
impl RegexMatcherBuilder {
    fn new() -> RegexMatcherBuilder { RegexMatcherBuilder {} }
}
`,
	})
	ResolveRustScopeCalls(g)
	targets := callTargetsFromRust(g, "crates/regex/src/matcher.rs::make")
	require.Contains(t, targets, "crates/regex/src/matcher.rs::RegexMatcherBuilder.new")
	require.NotContains(t, targets, "crates/pcre2/src/matcher.rs::RegexMatcherBuilder.new")
}
