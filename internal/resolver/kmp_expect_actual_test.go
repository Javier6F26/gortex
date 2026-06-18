package resolver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

func kmpEdgeBetween(g graph.Store, from, to string, kind graph.EdgeKind) *graph.Edge {
	for _, e := range g.GetOutEdges(from) {
		if e.To == to && e.Kind == kind && e.Meta != nil {
			if v, _ := e.Meta["via"].(string); v == kmpExpectActualVia {
				return e
			}
		}
	}
	return nil
}

func kmpNode(g graph.Store, id, name, file, role string, kind graph.NodeKind) {
	g.AddNode(&graph.Node{ID: id, Kind: kind, Name: name, FilePath: file, StartLine: 2, Language: "kotlin", Meta: map[string]any{"kmp_role": role}})
}

func TestResolveKMPExpectActual_PairsExpectToActual(t *testing.T) {
	g := graph.New()
	kmpNode(g, "common.kt::platformName", "platformName", "common.kt", "expect", graph.KindFunction)
	kmpNode(g, "android.kt::platformName", "platformName", "android.kt", "actual", graph.KindFunction)
	kmpNode(g, "ios.kt::platformName", "platformName", "ios.kt", "actual", graph.KindFunction)
	kmpNode(g, "common.kt::Platform", "Platform", "common.kt", "expect", graph.KindType)
	kmpNode(g, "android.kt::Platform", "Platform", "android.kt", "actual", graph.KindType)

	// 2 actuals for platformName + 1 for Platform = 3 pairs.
	assert.Equal(t, 3, ResolveKMPExpectActual(g))

	// Each actual implements the expect (find_implementations on the expect).
	require.NotNil(t, kmpEdgeBetween(g, "android.kt::platformName", "common.kt::platformName", graph.EdgeImplements), "android actual → expect")
	require.NotNil(t, kmpEdgeBetween(g, "ios.kt::platformName", "common.kt::platformName", graph.EdgeImplements), "ios actual → expect")
	// Reverse navigation edge.
	require.NotNil(t, kmpEdgeBetween(g, "common.kt::platformName", "android.kt::platformName", graph.EdgeReferences), "expect → actual")
	// Type pairing.
	require.NotNil(t, kmpEdgeBetween(g, "android.kt::Platform", "common.kt::Platform", graph.EdgeImplements), "actual type → expect type")

	e := kmpEdgeBetween(g, "android.kt::platformName", "common.kt::platformName", graph.EdgeImplements)
	assert.Equal(t, SynthKMPExpectActual, e.Meta[MetaSynthesizedBy])
}

func TestResolveKMPExpectActual_ExpectWithoutActualNoEdge(t *testing.T) {
	g := graph.New()
	kmpNode(g, "common.kt::lonely", "lonely", "common.kt", "expect", graph.KindFunction)
	assert.Equal(t, 0, ResolveKMPExpectActual(g))
}

func TestResolveKMPExpectActual_KindMismatchNoPair(t *testing.T) {
	g := graph.New()
	// Same name but different kinds must not pair.
	kmpNode(g, "common.kt::Thing", "Thing", "common.kt", "expect", graph.KindType)
	kmpNode(g, "android.kt::Thing", "Thing", "android.kt", "actual", graph.KindFunction)
	assert.Equal(t, 0, ResolveKMPExpectActual(g))
}

func TestResolveKMPExpectActual_Idempotent(t *testing.T) {
	g := graph.New()
	kmpNode(g, "common.kt::platformName", "platformName", "common.kt", "expect", graph.KindFunction)
	kmpNode(g, "android.kt::platformName", "platformName", "android.kt", "actual", graph.KindFunction)
	first := ResolveKMPExpectActual(g)
	second := ResolveKMPExpectActual(g)
	assert.Equal(t, first, second)

	count := 0
	for _, kind := range []graph.EdgeKind{graph.EdgeImplements, graph.EdgeReferences} {
		for e := range g.EdgesByKind(kind) {
			if e != nil && e.Meta != nil {
				if v, _ := e.Meta["via"].(string); v == kmpExpectActualVia {
					count++
				}
			}
		}
	}
	assert.Equal(t, 2, count)
}
