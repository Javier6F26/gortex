package resolver

import "github.com/zzet/gortex/internal/graph"

// swiftObjCBridgeVia marks a synthesized Swift↔ObjC bridge edge.
const swiftObjCBridgeVia = "swift.objc.bridge"

// ResolveSwiftObjCBridge is the framework-dispatch synthesizer for the
// Swift ↔ Objective-C bridge. The Objective-C extractor names each method
// node by its canonical selector (`moveFrom:to:`, `viewDidLoad`); the
// Swift extractor stamps the ObjC selector a method is exposed under
// (Meta["objc_selector"]) on every @objc method, derived from the
// argument labels or an explicit @objc(custom:) override. This pass joins
// the two: for each Swift @objc method whose selector matches an ObjC
// method node, it synthesizes a pair of EdgeReferences bridge edges (one
// each way) so navigation and find_usages span the language boundary —
// an ObjC selector call resolves to the Swift implementation, and a Swift
// method shows its ObjC-visible counterpart.
//
// Full recompute and idempotent: edges are re-derived from the selector
// metadata, graph.AddEdge dedupes by edge key, and graph.EvictFile drops
// the bridge in both directions when either side's file is reindexed.
// Edges ride at ast_inferred (selector-name matching is a heuristic, not
// a type-checked bind) and carry full synthesizer provenance.
//
// Returns the number of Swift methods bridged to at least one ObjC
// selector counterpart.
func ResolveSwiftObjCBridge(g graph.Store) int {
	if g == nil {
		return 0
	}

	objcBySelector := map[string][]*graph.Node{}
	swiftByName := map[string][]*graph.Node{}
	var swiftMethods []*graph.Node
	for _, n := range nodesByKindsOrAll(g, graph.KindMethod, graph.KindFunction) {
		if n == nil {
			continue
		}
		switch n.Language {
		case "objc":
			if n.Kind == graph.KindMethod && n.Name != "" {
				objcBySelector[n.Name] = append(objcBySelector[n.Name], n)
			}
		case "swift":
			if n.Name != "" {
				swiftByName[n.Name] = append(swiftByName[n.Name], n)
			}
			if n.Meta != nil {
				if sel, _ := n.Meta["objc_selector"].(string); sel != "" {
					swiftMethods = append(swiftMethods, n)
				}
			}
		}
	}
	if len(objcBySelector) == 0 || len(swiftByName) == 0 {
		return 0
	}

	var batch []*graph.Edge
	bridged := map[string]bool{}
	link := func(sm, om *graph.Node, sel string) {
		if sm.ID == om.ID {
			return
		}
		batch = append(batch,
			swiftObjCBridgeEdge(sm, om, sel),
			swiftObjCBridgeEdge(om, sm, sel),
		)
		bridged[sm.ID] = true
	}

	// Exact pass: a Swift @objc method declares the selector it is exposed
	// under; bind it to the ObjC method node of the same selector.
	for _, sm := range swiftMethods {
		sel, _ := sm.Meta["objc_selector"].(string)
		for _, om := range objcBySelector[sel] {
			link(sm, om, sel)
		}
	}

	// Candidate pass: for Swift methods without explicit selector metadata,
	// reverse the importer's naming rules — derive the Swift base names each
	// ObjC selector could surface under and bind by name. graph.AddEdge dedupes,
	// so a method matched by both passes counts once.
	for sel, objcNodes := range objcBySelector {
		for _, cand := range swiftObjCBaseNameCandidates(sel) {
			for _, sm := range swiftByName[cand] {
				for _, om := range objcNodes {
					link(sm, om, sel)
				}
			}
		}
	}

	for _, e := range batch {
		g.AddEdge(e)
	}
	return len(bridged)
}

// swiftObjCBridgeEdge builds one direction of the cross-language bridge.
func swiftObjCBridgeEdge(from, to *graph.Node, selector string) *graph.Edge {
	return &graph.Edge{
		From:            from.ID,
		To:              to.ID,
		Kind:            graph.EdgeReferences,
		FilePath:        from.FilePath,
		Line:            from.StartLine,
		Confidence:      0.6,
		ConfidenceLabel: graph.ConfidenceLabelFor(graph.EdgeReferences, 0.6),
		Origin:          graph.OriginASTInferred,
		Meta: map[string]any{
			"via":              swiftObjCBridgeVia,
			"objc_selector":    selector,
			MetaSynthesizedBy:  SynthSwiftObjC,
			MetaProvenance:     ProvenanceHeuristic,
			"bridge_from_lang": from.Language,
			"bridge_to_lang":   to.Language,
		},
	}
}
