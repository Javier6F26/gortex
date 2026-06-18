package resolver

import "github.com/zzet/gortex/internal/graph"

// Intra-process dispatch synthesizers (closure-collection, observer-channel,
// event-channel, store-factory) pair a dispatcher with a registrar/callback by
// a bare name — a collection field, a channel/event topic, a store binding.
// Those names are generic ("handlers", "items", "update", "submit") and recur
// across unrelated repositories, so in a multi-repo graph an unguarded pairing
// fans a dispatcher in one repo out to a same-named registrar in another — a
// false edge a single-repo tool could never even produce.
//
// sameDispatchBoundary is the gate that turns that multi-repo reach from a
// precision liability into a strict win: two endpoints may be paired only when
// they share the graph's hard boundary, WorkspaceID — which is "" for a
// single-repo graph (always paired) and shared across a monorepo's member
// repos (still paired) but differs between independent projects (never paired).
// Genuinely cross-language / cross-repo bridges (gRPC, Temporal, the native
// bridges) are deliberately NOT routed through this gate and stay global.
func sameDispatchBoundary(a, b *graph.Node) bool {
	return a != nil && b != nil && a.WorkspaceID == b.WorkspaceID
}

// sameDispatchBoundaryIDs resolves two node IDs and reports whether they share
// the dispatch boundary. Unknown nodes never pair.
func sameDispatchBoundaryIDs(g graph.Store, aID, bID string) bool {
	return sameDispatchBoundary(g.GetNode(aID), g.GetNode(bID))
}

// sameBoundaryCandidates filters cands to those sharing the caller's hard graph
// boundary, so a binding/action name reused across unrelated repos cannot bind
// a call to a target in a different workspace. Returns cands unchanged when the
// caller's node (and thus its workspace) is unknown, so single-repo resolution
// is never weakened.
func sameBoundaryCandidates(g graph.Store, callerID string, cands []*graph.Node) []*graph.Node {
	caller := g.GetNode(callerID)
	if caller == nil {
		return cands
	}
	out := make([]*graph.Node, 0, len(cands))
	for _, c := range cands {
		if c != nil && c.WorkspaceID == caller.WorkspaceID {
			out = append(out, c)
		}
	}
	return out
}
