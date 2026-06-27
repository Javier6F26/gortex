package resolver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

func swiftMethodNode(g graph.Store, id, selector string) {
	g.AddNode(&graph.Node{
		ID: id, Kind: graph.KindMethod, Name: lastSeg(id), FilePath: "ios/App.swift", StartLine: 10,
		Language: "swift", Meta: map[string]any{"objc_selector": selector},
	})
}

func objcMethodNode(g graph.Store, id, selector string) {
	g.AddNode(&graph.Node{
		ID: id, Kind: graph.KindMethod, Name: selector, FilePath: "ios/Legacy.m", StartLine: 5,
		Language: "objc",
	})
}

func bridgeEdgeBetween(g graph.Store, from, to string) *graph.Edge {
	for _, e := range g.GetOutEdges(from) {
		if e.To == to && e.Kind == graph.EdgeReferences && e.Meta != nil {
			if v, _ := e.Meta["via"].(string); v == swiftObjCBridgeVia {
				return e
			}
		}
	}
	return nil
}

func TestResolveSwiftObjCBridge_BindsSelector(t *testing.T) {
	g := graph.New()
	swiftMethodNode(g, "ios/App.swift::Mover.move", "moveFrom:to:")
	objcMethodNode(g, "ios/Legacy.m::moveFrom:to:", "moveFrom:to:")

	n := ResolveSwiftObjCBridge(g)
	assert.Equal(t, 1, n)

	fwd := bridgeEdgeBetween(g, "ios/App.swift::Mover.move", "ios/Legacy.m::moveFrom:to:")
	require.NotNil(t, fwd, "swift→objc bridge edge")
	assert.Equal(t, "moveFrom:to:", fwd.Meta["objc_selector"])
	assert.Equal(t, SynthSwiftObjC, fwd.Meta[MetaSynthesizedBy])
	assert.Equal(t, graph.OriginASTInferred, fwd.Origin)

	rev := bridgeEdgeBetween(g, "ios/Legacy.m::moveFrom:to:", "ios/App.swift::Mover.move")
	require.NotNil(t, rev, "objc→swift bridge edge (bidirectional)")
}

func TestResolveSwiftObjCBridge_CandidateBridge(t *testing.T) {
	g := graph.New()
	// A Swift method with no @objc selector metadata — only its base name.
	g.AddNode(&graph.Node{
		ID: "ios/VC.swift::VC.cellForRow", Kind: graph.KindMethod, Name: "cellForRow",
		FilePath: "ios/VC.swift", StartLine: 20, Language: "swift",
	})
	objcMethodNode(g, "ios/Legacy.m::cellForRowAtIndexPath:", "cellForRowAtIndexPath:")

	assert.Equal(t, 1, ResolveSwiftObjCBridge(g))

	fwd := bridgeEdgeBetween(g, "ios/VC.swift::VC.cellForRow", "ios/Legacy.m::cellForRowAtIndexPath:")
	require.NotNil(t, fwd, "candidate swift→objc bridge")
	assert.Equal(t, "cellForRowAtIndexPath:", fwd.Meta["objc_selector"])
	rev := bridgeEdgeBetween(g, "ios/Legacy.m::cellForRowAtIndexPath:", "ios/VC.swift::VC.cellForRow")
	require.NotNil(t, rev, "candidate objc→swift bridge (bidirectional)")
}

func TestSwiftObjCBaseNameCandidates(t *testing.T) {
	cases := map[string][]string{
		"cellForRowAtIndexPath:":           {"cellForRowAtIndexPath", "cellForRow"},
		"moveFrom:to:":                     {"moveFrom", "move"},
		"initWithFrame:":                   {"initWithFrame"},
		"tableView:numberOfRowsInSection:": {"tableView"},
		"viewDidLoad":                      {"viewDidLoad"},
		"dataForKey:":                      {"dataForKey", "data"},
	}
	for sel, want := range cases {
		assert.ElementsMatch(t, want, swiftObjCBaseNameCandidates(sel), "selector %q", sel)
	}
}

func TestResolveSwiftObjCBridge_PropertyAccessors(t *testing.T) {
	g := graph.New()
	// A Swift @objc property exposes a getter (`title`) and setter (`setTitle:`).
	g.AddNode(&graph.Node{
		ID: "ios/W.swift::Widget.title", Kind: graph.KindField, Name: "title",
		FilePath: "ios/W.swift", StartLine: 2, Language: "swift",
		Meta: map[string]any{"objc_selector": "title", "objc_setter_selector": "setTitle:"},
	})
	// Native ObjC accessor methods.
	objcMethodNode(g, "ios/W.m::title", "title")
	objcMethodNode(g, "ios/W.m::setTitle:", "setTitle:")

	// One Swift declaration bridged (to both accessors).
	assert.Equal(t, 1, ResolveSwiftObjCBridge(g))
	require.NotNil(t, bridgeEdgeBetween(g, "ios/W.swift::Widget.title", "ios/W.m::title"), "getter bridge")
	require.NotNil(t, bridgeEdgeBetween(g, "ios/W.swift::Widget.title", "ios/W.m::setTitle:"), "setter bridge")
	require.NotNil(t, bridgeEdgeBetween(g, "ios/W.m::setTitle:", "ios/W.swift::Widget.title"), "reverse setter bridge")
}

func TestResolveSwiftObjCBridge_SuppressGenericCandidates(t *testing.T) {
	// Bare NSObject selectors yield no candidates at all.
	for _, sel := range []string{"init", "copy", "description", "isEqual:", "hash"} {
		assert.Empty(t, swiftObjCBaseNameCandidates(sel), "generic selector %q must not produce candidates", sel)
	}
	// A specific selector keeps its verbatim candidate but drops the generic short form.
	assert.ElementsMatch(t, []string{"initWithFrame"}, swiftObjCBaseNameCandidates("initWithFrame:"))
}

func TestResolveSwiftObjCBridge_SuppressGenericBridge(t *testing.T) {
	g := graph.New()
	// Swift methods named like NSObject selectors must not candidate-bridge.
	g.AddNode(&graph.Node{ID: "ios/A.swift::A.init", Kind: graph.KindMethod, Name: "init", FilePath: "ios/A.swift", StartLine: 3, Language: "swift"})
	g.AddNode(&graph.Node{ID: "ios/A.swift::A.description", Kind: graph.KindMethod, Name: "description", FilePath: "ios/A.swift", StartLine: 6, Language: "swift"})
	objcMethodNode(g, "ios/B.m::init", "init")
	objcMethodNode(g, "ios/B.m::description", "description")

	assert.Equal(t, 0, ResolveSwiftObjCBridge(g))
	assert.Nil(t, bridgeEdgeBetween(g, "ios/A.swift::A.init", "ios/B.m::init"))
	assert.Nil(t, bridgeEdgeBetween(g, "ios/A.swift::A.description", "ios/B.m::description"))
}

func TestResolveSwiftObjCBridge_ExplicitSelector(t *testing.T) {
	g := graph.New()
	swiftMethodNode(g, "ios/App.swift::Mover.moveCustom", "customMove:")
	objcMethodNode(g, "ios/Legacy.m::customMove:", "customMove:")
	objcMethodNode(g, "ios/Legacy.m::unrelated:", "unrelated:")

	assert.Equal(t, 1, ResolveSwiftObjCBridge(g))
	assert.NotNil(t, bridgeEdgeBetween(g, "ios/App.swift::Mover.moveCustom", "ios/Legacy.m::customMove:"))
	assert.Nil(t, bridgeEdgeBetween(g, "ios/App.swift::Mover.moveCustom", "ios/Legacy.m::unrelated:"))
}

func TestResolveSwiftObjCBridge_NoMatchNoEdge(t *testing.T) {
	g := graph.New()
	swiftMethodNode(g, "ios/App.swift::Mover.move", "moveFrom:to:")
	objcMethodNode(g, "ios/Legacy.m::other:", "other:")
	assert.Equal(t, 0, ResolveSwiftObjCBridge(g))
}

func TestResolveSwiftObjCBridge_Idempotent(t *testing.T) {
	g := graph.New()
	swiftMethodNode(g, "ios/App.swift::Mover.move", "moveFrom:to:")
	objcMethodNode(g, "ios/Legacy.m::moveFrom:to:", "moveFrom:to:")
	first := ResolveSwiftObjCBridge(g)
	second := ResolveSwiftObjCBridge(g)
	assert.Equal(t, first, second)
	// Exactly two bridge edges (one each direction) survive dedup.
	count := 0
	for e := range g.EdgesByKind(graph.EdgeReferences) {
		if e != nil && e.Meta != nil {
			if v, _ := e.Meta["via"].(string); v == swiftObjCBridgeVia {
				count++
			}
		}
	}
	assert.Equal(t, 2, count)
}

func TestResolveSwiftObjCBridge_ProtocolConformance(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{
		ID: "ios/P.swift::Drawable", Kind: graph.KindInterface, Name: "Drawable",
		FilePath: "ios/P.swift", StartLine: 1, Language: "swift",
		Meta: map[string]any{"objc": true},
	})
	g.AddNode(&graph.Node{
		ID: "ios/C.m::Widget", Kind: graph.KindType, Name: "Widget",
		FilePath: "ios/C.m", StartLine: 1, Language: "objc",
		Meta: map[string]any{"objc_protocols": "Drawable"},
	})

	ResolveSwiftObjCBridge(g)

	var impl *graph.Edge
	for _, e := range g.GetOutEdges("ios/C.m::Widget") {
		if e.To == "ios/P.swift::Drawable" && e.Kind == graph.EdgeImplements {
			impl = e
		}
	}
	require.NotNil(t, impl, "ObjC class adopting a Swift @objc protocol should get EdgeImplements")
	assert.Equal(t, SynthSwiftObjC, impl.Meta[MetaSynthesizedBy])
	assert.Equal(t, "objc", impl.Meta["bridge_from_lang"])
	assert.Equal(t, "swift", impl.Meta["bridge_to_lang"])
}

func TestResolveSwiftObjCBridge_NonObjCProtocolNotBridged(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{
		ID: "ios/P.swift::Plain", Kind: graph.KindInterface, Name: "Plain",
		FilePath: "ios/P.swift", StartLine: 1, Language: "swift",
	})
	g.AddNode(&graph.Node{
		ID: "ios/C.m::Widget", Kind: graph.KindType, Name: "Widget",
		FilePath: "ios/C.m", StartLine: 1, Language: "objc",
		Meta: map[string]any{"objc_protocols": "Plain"},
	})

	ResolveSwiftObjCBridge(g)

	for _, e := range g.GetOutEdges("ios/C.m::Widget") {
		if e.To == "ios/P.swift::Plain" && e.Kind == graph.EdgeImplements {
			t.Errorf("a non-@objc Swift protocol must not be bridged")
		}
	}
}
