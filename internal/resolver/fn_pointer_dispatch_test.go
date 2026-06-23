package resolver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

func fnDef(g *graph.Graph, id, file, name string) {
	g.AddNode(&graph.Node{ID: id, Kind: graph.KindFunction, Name: name, FilePath: file, Language: "c"})
}

func fnPtrReg(g *graph.Graph, file, st, field, fn string) {
	g.AddEdge(&graph.Edge{From: file, To: "unresolved::*." + fn, Kind: graph.EdgeReferences, FilePath: file,
		Meta: map[string]any{"via": fnPtrRegVia, "fnptr_struct": st, "fnptr_field": field, "fnptr_fn": fn}})
}

func fnPtrCopy(g *graph.Graph, file, toSt, toField, fromSt, fromField string) {
	g.AddEdge(&graph.Edge{From: file, To: "unresolved::*." + fromField, Kind: graph.EdgeReferences, FilePath: file,
		Meta: map[string]any{"via": fnPtrRegVia, "fnptr_struct": toSt, "fnptr_field": toField, "fnptr_copy_struct": fromSt, "fnptr_copy_field": fromField}})
}

func fnPtrDispatch(g *graph.Graph, fromID, file, st, field string) {
	if g.GetNode(fromID) == nil {
		g.AddNode(&graph.Node{ID: fromID, Kind: graph.KindFunction, Name: lastSeg(fromID), FilePath: file, Language: "c"})
	}
	g.AddEdge(&graph.Edge{From: fromID, To: "unresolved::*." + field, Kind: graph.EdgeCalls, FilePath: file,
		Meta: map[string]any{"via": fnPtrDispatchVia, "fnptr_struct": st, "fnptr_field": field}})
}

func synthFnPtrEdge(g graph.Store, from, to string) *graph.Edge {
	for e := range g.EdgesByKind(graph.EdgeCalls) {
		if e == nil || e.From != from || e.To != to || e.Meta == nil {
			continue
		}
		if by, _ := e.Meta[MetaSynthesizedBy].(string); by == SynthFnPointerDispatch {
			return e
		}
	}
	return nil
}

func TestResolveFnPointerDispatch_CommandTableFanOut(t *testing.T) {
	g := graph.New()
	fnDef(g, "cmds.c::cmd_add", "cmds.c", "cmd_add")
	fnDef(g, "cmds.c::cmd_rm", "cmds.c", "cmd_rm")
	fnDef(g, "cmds.c::run", "cmds.c", "run")
	fnPtrReg(g, "cmds.c", "cmd", "fn", "cmd_add")
	fnPtrReg(g, "cmds.c", "cmd", "fn", "cmd_rm")
	fnPtrDispatch(g, "cmds.c::run", "cmds.c", "cmd", "fn")

	n := ResolveFnPointerDispatch(g)
	require.Equal(t, 2, n, "the dispatch fans out to both registered commands")
	a := synthFnPtrEdge(g, "cmds.c::run", "cmds.c::cmd_add")
	require.NotNil(t, a)
	assert.Equal(t, fnPointerConfidence, a.Confidence)
	assert.Equal(t, ProvenanceHeuristic, a.Meta[MetaProvenance])
	assert.NotNil(t, synthFnPtrEdge(g, "cmds.c::run", "cmds.c::cmd_rm"))
}

func TestResolveFnPointerDispatch_FieldCopyFixpoint(t *testing.T) {
	// op_x is registered to (B, h); A.h is copied from B.h; a dispatch on A
	// must reach op_x through the fixpoint.
	g := graph.New()
	fnDef(g, "ops.c::op_x", "ops.c", "op_x")
	fnDef(g, "ops.c::run_a", "ops.c", "run_a")
	fnPtrReg(g, "ops.c", "B", "h", "op_x")
	fnPtrCopy(g, "ops.c", "A", "h", "B", "h")
	fnPtrDispatch(g, "ops.c::run_a", "ops.c", "A", "h")

	n := ResolveFnPointerDispatch(g)
	require.Equal(t, 1, n)
	assert.NotNil(t, synthFnPtrEdge(g, "ops.c::run_a", "ops.c::op_x"),
		"the field-copy fixpoint propagates B.h's function to A.h")
}

func TestResolveFnPointerDispatch_UnregisteredSlotStaysPlaceholder(t *testing.T) {
	g := graph.New()
	fnDef(g, "x.c::handler", "x.c", "handler")
	fnPtrReg(g, "x.c", "S", "f", "handler")
	// dispatch on a different slot with no registrations.
	fnPtrDispatch(g, "x.c::run", "x.c", "S", "other")

	assert.Equal(t, 0, ResolveFnPointerDispatch(g))
}
