package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

// manifestEntryByID finds the manifest entry for a node ID.
func manifestEntryByID(entries []map[string]any, id string) map[string]any {
	for _, e := range entries {
		if e["id"] == id {
			return e
		}
	}
	return nil
}

// writeManifestFixture writes a multi-line source file whose full and
// compressed renderings differ, and returns its absolute path.
func writeManifestFixture(t *testing.T, dir, name, body string) string {
	t.Helper()
	abs := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(abs, []byte(body), 0o644))
	return abs
}

// TestManifest_SkeletonizesLargeInterfaceFamily proves a focus interface
// with more than the sibling threshold of implementors is skeletonized
// (compressed, with sibling_count surfaced) rather than embedded full,
// while a control interface with a single implementor stays full.
func TestManifest_SkeletonizesLargeInterfaceFamily(t *testing.T) {
	srv, dir := setupTestServer(t)
	g := srv.graph
	ctx := context.Background()

	// Body source with a real function body so full != compressed.
	bigBody := `package fam

// BigFamily is implemented by many interchangeable types.
type BigFamily interface {
	Do(x int) int
	Name() string
}
`
	soleBody := `package fam

// Lonely has a single implementation.
type Lonely interface {
	Only() error
}
`
	bigPath := writeManifestFixture(t, dir, "big.go", bigBody)
	solePath := writeManifestFixture(t, dir, "sole.go", soleBody)

	add := func(id, name string, kind graph.NodeKind, file string, start, end int) {
		g.AddNode(&graph.Node{
			ID: id, Kind: kind, Name: name,
			FilePath: file, Language: "go",
			StartLine: start, EndLine: end,
		})
	}
	impl := func(from, to string) {
		g.AddEdge(&graph.Edge{From: from, To: to, Kind: graph.EdgeImplements, Origin: graph.OriginLSPResolved, FilePath: "x.go"})
	}

	// The big interface spans lines 4-7 of big.go.
	add("big.go::BigFamily", "BigFamily", graph.KindInterface, bigPath, 4, 7)
	// Five implementors — well past the threshold of 3.
	for _, n := range []string{"A", "B", "C", "D", "E"} {
		id := "impl.go::" + n
		add(id, n, graph.KindType, bigPath, 1, 1)
		impl(id, "big.go::BigFamily")
	}

	// The sole interface spans lines 4-6 of sole.go, with one implementor.
	add("sole.go::Lonely", "Lonely", graph.KindInterface, solePath, 4, 6)
	add("impl.go::Single", "Single", graph.KindType, solePath, 1, 1)
	impl("impl.go::Single", "sole.go::Lonely")

	focus := []*graph.Node{
		g.GetNode("big.go::BigFamily"),
		g.GetNode("sole.go::Lonely"),
	}
	require.NotNil(t, focus[0])
	require.NotNil(t, focus[1])

	mani := srv.buildContextManifest(ctx, focus, nil, 8000)
	entries, _ := mani["entries"].([]map[string]any)
	require.NotEmpty(t, entries)

	big := manifestEntryByID(entries, "big.go::BigFamily")
	require.NotNil(t, big, "big-family interface must be in the manifest")
	assert.Equal(t, "focus", big["tier"])
	assert.Equal(t, true, big["compressed"],
		"a large interface family must be skeletonized (compressed)")
	sc, _ := big["sibling_count"].(int)
	assert.Equal(t, 5, sc, "sibling_count must report the implementor family size")

	sole := manifestEntryByID(entries, "sole.go::Lonely")
	require.NotNil(t, sole, "sole interface must be in the manifest")
	assert.Equal(t, "focus", sole["tier"])
	assert.Equal(t, false, sole["compressed"],
		"a single-implementor interface must stay full, not skeletonized")
}

// TestManifest_SkeletonizesLargeMethodFamily proves a focus method with
// more than the threshold of overriders is skeletonized.
func TestManifest_SkeletonizesLargeMethodFamily(t *testing.T) {
	srv, dir := setupTestServer(t)
	g := srv.graph
	ctx := context.Background()

	body := `package fam

// Base.Run is overridden by many subtypes.
func (b *Base) Run(n int) int {
	total := 0
	for i := 0; i < n; i++ {
		total += i
	}
	return total
}
`
	p := writeManifestFixture(t, dir, "base.go", body)
	g.AddNode(&graph.Node{
		ID: "base.go::Base.Run", Kind: graph.KindMethod, Name: "Base.Run",
		FilePath: p, Language: "go", StartLine: 4, EndLine: 10,
	})
	for _, n := range []string{"W", "X", "Y", "Z"} {
		id := "ov.go::" + n + ".Run"
		g.AddNode(&graph.Node{ID: id, Kind: graph.KindMethod, Name: n + ".Run", FilePath: p, Language: "go", StartLine: 1, EndLine: 1})
		// EdgeOverrides points child -> parent; FindOverrides reads
		// the inverse to collect children.
		g.AddEdge(&graph.Edge{From: id, To: "base.go::Base.Run", Kind: graph.EdgeOverrides, Origin: graph.OriginLSPResolved, FilePath: "x.go"})
	}

	focus := []*graph.Node{g.GetNode("base.go::Base.Run")}
	require.NotNil(t, focus[0])

	mani := srv.buildContextManifest(ctx, focus, nil, 8000)
	entries, _ := mani["entries"].([]map[string]any)
	e := manifestEntryByID(entries, "base.go::Base.Run")
	require.NotNil(t, e)
	assert.Equal(t, true, e["compressed"], "an over-threshold method family must skeletonize")
	sc, _ := e["sibling_count"].(int)
	assert.Equal(t, 4, sc)
}

// TestManifest_SiblingCountCounting verifies the per-kind counting rules
// of manifestSiblingCount directly.
func TestManifest_SiblingCountCounting(t *testing.T) {
	srv, _ := setupTestServer(t)
	g := srv.graph
	ctx := context.Background()

	g.AddNode(&graph.Node{ID: "i.go::Iface", Kind: graph.KindInterface, Name: "Iface", FilePath: "i.go", Language: "go"})
	for _, n := range []string{"P", "Q"} {
		id := "t.go::" + n
		g.AddNode(&graph.Node{ID: id, Kind: graph.KindType, Name: n, FilePath: "t.go", Language: "go"})
		g.AddEdge(&graph.Edge{From: id, To: "i.go::Iface", Kind: graph.EdgeImplements, Origin: graph.OriginLSPResolved, FilePath: "x.go"})
	}
	// Interface family size = 2 implementors.
	assert.Equal(t, 2, srv.manifestSiblingCount(ctx, g.GetNode("i.go::Iface")))
	// A concrete type's co-implementor count excludes itself: P sees Q.
	assert.Equal(t, 1, srv.manifestSiblingCount(ctx, g.GetNode("t.go::P")))
	// A function is never a polymorphic family.
	g.AddNode(&graph.Node{ID: "f.go::Fn", Kind: graph.KindFunction, Name: "Fn", FilePath: "f.go", Language: "go"})
	assert.Equal(t, 0, srv.manifestSiblingCount(ctx, g.GetNode("f.go::Fn")))
	assert.False(t, skeletonizableKind(graph.KindFunction))
	assert.True(t, skeletonizableKind(graph.KindInterface))
	assert.True(t, skeletonizableKind(graph.KindType))
	assert.True(t, skeletonizableKind(graph.KindMethod))
}
