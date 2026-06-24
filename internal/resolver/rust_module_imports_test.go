package resolver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zzet/gortex/internal/graph"
)

func rustUse(g graph.Store, from, usePath string) *graph.Edge {
	e := &graph.Edge{From: from, To: "unresolved::import::" + usePath, Kind: graph.EdgeImports}
	g.AddEdge(e)
	return e
}

// TestResolveRustModuleImports_CrateModFile pins `use crate::foo::bar;` binding
// to src/foo/mod.rs (the file defining the foo module that contains bar).
func TestResolveRustModuleImports_CrateModFile(t *testing.T) {
	g := graph.New()
	seedFile(g, "src/lib.rs", "rust")
	seedFile(g, "src/foo/mod.rs", "rust")
	e := rustUse(g, "src/lib.rs", "crate/foo/bar")

	ResolveRustScopeCalls(g)

	assert.Equal(t, "src/foo/mod.rs", e.To)
	assert.Equal(t, graph.OriginASTResolved, e.Origin)
	assert.Equal(t, "rust_module", e.Meta["resolved_via"])
}

// TestResolveRustModuleImports_CrateFileModule pins binding to the `foo.rs`
// file form (not a mod.rs directory module).
func TestResolveRustModuleImports_CrateFileModule(t *testing.T) {
	g := graph.New()
	seedFile(g, "src/lib.rs", "rust")
	seedFile(g, "src/utils.rs", "rust")
	e := rustUse(g, "src/lib.rs", "crate/utils/helper")

	ResolveRustScopeCalls(g)

	assert.Equal(t, "src/utils.rs", e.To)
}

// TestResolveRustModuleImports_SelfAnchor pins `use self::baz;` anchoring at the
// caller's own module directory.
func TestResolveRustModuleImports_SelfAnchor(t *testing.T) {
	g := graph.New()
	seedFile(g, "src/foo/mod.rs", "rust")
	seedFile(g, "src/foo/baz.rs", "rust")
	e := rustUse(g, "src/foo/mod.rs", "self/baz")

	ResolveRustScopeCalls(g)

	assert.Equal(t, "src/foo/baz.rs", e.To)
}

// TestResolveRustModuleImports_SuperAnchor pins `use super::bar;` walking one
// module directory up from the caller.
func TestResolveRustModuleImports_SuperAnchor(t *testing.T) {
	g := graph.New()
	seedFile(g, "src/foo/mod.rs", "rust")
	seedFile(g, "src/bar.rs", "rust")
	e := rustUse(g, "src/foo/mod.rs", "super/bar")

	ResolveRustScopeCalls(g)

	assert.Equal(t, "src/bar.rs", e.To)
}

// TestResolveRustModuleImports_AmbiguousRefused pins that a module present as
// both foo.rs and foo/mod.rs is refused.
func TestResolveRustModuleImports_AmbiguousRefused(t *testing.T) {
	g := graph.New()
	seedFile(g, "src/lib.rs", "rust")
	seedFile(g, "src/foo.rs", "rust")
	seedFile(g, "src/foo/mod.rs", "rust")
	e := rustUse(g, "src/lib.rs", "crate/foo")

	ResolveRustScopeCalls(g)

	assert.Equal(t, "unresolved::import::crate/foo", e.To, "ambiguous foo.rs vs foo/mod.rs refused")
}

// TestResolveRustModuleImports_ExternalCrateUntouched pins that a non-anchored
// external crate path is left alone.
func TestResolveRustModuleImports_ExternalCrateUntouched(t *testing.T) {
	g := graph.New()
	seedFile(g, "src/lib.rs", "rust")
	e := rustUse(g, "src/lib.rs", "serde/Serialize")

	ResolveRustScopeCalls(g)

	assert.Equal(t, "unresolved::import::serde/Serialize", e.To, "external crate path left alone")
}
