package indexer

import (
	"strings"

	"github.com/zzet/gortex/internal/graph"
)

// materializeDataflowParams runs after the regular call resolver
// pass to lift the placeholder targets carried by EdgeArgOf and
// EdgeReturnsTo edges to concrete graph IDs. The Go dataflow
// extractor (see internal/parser/languages/go_dataflow.go) emits
// these edges with an `unresolved::` text on the side that
// references the callee — exactly the shape the call resolver
// already knows how to lift. After Resolver.ResolveAll has run
// every placeholder side has been rewritten to a real function /
// method node ID; this pass then:
//
//  1. EdgeArgOf — joins the now-resolved To (a function/method
//     node) against its incoming EdgeParamOf edges to find the
//     param node at the recorded position (Meta["arg_position"]),
//     and rewrites the edge target to the param node ID. When no
//     matching param exists (variadic position past the declared
//     count, signature mismatch from extern callees, etc.) the
//     edge stays pointed at the function node — still a useful
//     dataflow hop.
//
//  2. EdgeReturnsTo — joins the placeholder From (currently the
//     enclosing caller's function ID) against the resolved
//     EdgeCalls edge from the same caller at the same line,
//     and rewrites From to the resolved callee. Falls back to
//     leaving the placeholder in place when no matching call
//     edge can be found (rare; usually means the call resolver
//     declined to lift the call edge too).
//
// Both rewrite paths use graph.RemoveEdge + graph.AddEdge so the
// shard buckets / inverted indexes stay consistent with the new
// (From, To, Kind, Line) tuple. Edges whose Meta no longer
// matches their state are stripped of the dataflow markers so a
// re-run of this pass becomes a no-op.
func (idx *Indexer) materializeDataflowParams() {
	g := idx.graph
	// Only arg_of / returns_to edges are rewritten here. Fetch exactly
	// those kinds — each an edges_by_kind index probe on the sqlite
	// backend — instead of scanning (and meta-decoding) the whole edge
	// set; every other edge in the graph is irrelevant to this pass.
	for e := range g.EdgesByKind(graph.EdgeArgOf) {
		rewriteArgOf(g, e)
	}
	for e := range g.EdgesByKind(graph.EdgeReturnsTo) {
		rewriteReturnsTo(g, e)
	}
}

// materializeDataflowParamsForFile is the single-file equivalent of
// materializeDataflowParams, used on the incremental (fsnotify /
// edit_file) re-index path so a one-line edit doesn't scan the whole
// edge set. fileEdges is the file's freshly-extracted edge slice
// (result.Edges from indexFile); only its From endpoints are read, so
// stale To/From values from before resolution don't matter.
//
// A file's arg_of / returns_to From is NOT always a node in the file,
// so node membership alone is insufficient. Two From classes exist:
//   - file nodes: returns_to's From is the caller function, and an
//     arg_of whose argument is a bare in-scope identifier has its From
//     rewritten by the resolver to that local/param — GetFileNodes
//     covers both.
//   - synthetic ids: arg_of for a selector (obj.Field), package-
//     qualified (pkg.V), global, or nested-call (f(g())) argument keeps
//     a synthetic `unresolved::` / `external::` From that never becomes
//     a file node. The resolver leaves these untouched, so the id the
//     extractor emitted (still present in fileEdges) is the id in the
//     graph.
//
// Probing the union of both, then keeping only edges whose FilePath is
// this file, yields exactly the arg_of+returns_to set the whole-graph
// pass would touch for it — faithful, not approximate. Each rewrite
// needs only the edge plus a targeted callee lookup (paramNodeAtPosition
// / findCallTarget). The batch path (Resolver.ResolveAll) still runs the
// whole-graph variant once, where amortising one scan over many files
// is the right trade.
func (idx *Indexer) materializeDataflowParamsForFile(graphPath string, fileEdges []*graph.Edge) {
	g := idx.graph
	fromSet := make(map[string]struct{})
	for _, n := range g.GetFileNodes(graphPath) {
		if n != nil && n.ID != "" {
			fromSet[n.ID] = struct{}{}
		}
	}
	for _, e := range fileEdges {
		if e != nil && (e.Kind == graph.EdgeArgOf || e.Kind == graph.EdgeReturnsTo) && e.From != "" {
			fromSet[e.From] = struct{}{}
		}
	}
	if len(fromSet) == 0 {
		return
	}
	froms := make([]string, 0, len(fromSet))
	for id := range fromSet {
		froms = append(froms, id)
	}
	// A synthetic From can be shared across files, so restrict the rewrite
	// to edges this file actually emitted: every arg_of / returns_to edge
	// carries its call-site FilePath, so the filter keeps the set exactly
	// the file's own.
	for _, edges := range g.GetOutEdgesByNodeIDs(froms) {
		for _, e := range edges {
			if e == nil || e.FilePath != graphPath {
				continue
			}
			switch e.Kind {
			case graph.EdgeArgOf:
				rewriteArgOf(g, e)
			case graph.EdgeReturnsTo:
				rewriteReturnsTo(g, e)
			}
		}
	}
}

// rewriteArgOf walks the resolved callee's incoming param_of edges
// and lifts the edge target from the function node to the param
// node at the recorded position. Edges that already point at a
// param node are left alone.
func rewriteArgOf(g graph.Store, e *graph.Edge) {
	if e == nil || e.Meta == nil {
		return
	}
	pos, ok := argPositionFromMeta(e.Meta)
	if !ok {
		return
	}
	to := e.To
	if strings.Contains(to, "#param:") {
		return
	}
	if strings.HasPrefix(to, "unresolved::") || strings.HasPrefix(to, "external::") {
		return
	}
	calleeID := to
	paramID := paramNodeAtPosition(g, calleeID, pos)
	if paramID == "" {
		return
	}
	oldTo := e.To
	g.RemoveEdge(e.From, oldTo, e.Kind)
	e.To = paramID
	g.AddEdge(e)
}

// rewriteReturnsTo lifts the placeholder From by joining on the
// resolved EdgeCalls edge from the same caller and line.
func rewriteReturnsTo(g graph.Store, e *graph.Edge) {
	if e == nil || e.Meta == nil {
		return
	}
	if _, ok := e.Meta["returns_to_call"]; !ok {
		return
	}
	callLine, _ := intFromMeta(e.Meta, "call_line")
	if callLine == 0 {
		callLine = e.Line
	}
	callerID := e.From
	calleeText, _ := e.Meta["callee_target"].(string)
	resolvedCallee := findCallTarget(g, callerID, callLine, calleeText)
	if resolvedCallee == "" {
		return
	}
	oldFrom := e.From
	g.RemoveEdge(oldFrom, e.To, e.Kind)
	e.From = resolvedCallee
	g.AddEdge(e)
}

// findCallTarget returns the resolved To of the EdgeCalls edge
// originating from callerID at the given line. When `calleeText`
// is non-empty it's used as a tie-breaker against the original
// unresolved target string so we don't lift to the wrong call when
// two calls live on the same line. Falls back to the first match
// otherwise.
func findCallTarget(g graph.Store, callerID string, line int, calleeText string) string {
	out := g.GetOutEdges(callerID)
	var fallback string
	for _, e := range out {
		if e.Kind != graph.EdgeCalls {
			continue
		}
		if line != 0 && e.Line != line {
			continue
		}
		if strings.HasPrefix(e.To, "unresolved::") {
			continue
		}
		if calleeText != "" && callTargetMatches(e, calleeText) {
			return e.To
		}
		if fallback == "" {
			fallback = e.To
		}
	}
	return fallback
}

// callTargetMatches reports whether a resolved call edge's text
// shape lines up with the dataflow edge's recorded callee_target.
// We compare the trailing path component of the resolved To
// against the unresolved::… form used at extraction time. Used as
// a same-line tie-breaker when more than one call lives on a
// single source line (e.g. `f(g())`).
func callTargetMatches(call *graph.Edge, calleeText string) bool {
	if call == nil || calleeText == "" {
		return false
	}
	bare := strings.TrimPrefix(calleeText, "unresolved::")
	bare = strings.TrimPrefix(bare, "extern::")
	bare = strings.TrimPrefix(bare, "*.")
	if bare == "" {
		return false
	}
	to := call.To
	if i := strings.LastIndex(to, "::"); i >= 0 {
		to = to[i+2:]
	}
	if i := strings.LastIndex(to, "."); i >= 0 {
		to = to[i+1:]
	}
	return to == bare
}

// paramNodeAtPosition returns the param node ID with the recorded
// position attached to ownerID via EdgeParamOf.
func paramNodeAtPosition(g graph.Store, ownerID string, pos int) string {
	in := g.GetInEdges(ownerID)
	for _, e := range in {
		if e.Kind != graph.EdgeParamOf {
			continue
		}
		n := g.GetNode(e.From)
		if n == nil || n.Kind != graph.KindParam {
			continue
		}
		p, ok := intFromMeta(n.Meta, "position")
		if !ok {
			continue
		}
		if p == pos {
			return n.ID
		}
	}
	return ""
}

// argPositionFromMeta extracts the recorded argument position. The
// metadata roundtrip can yield int or float64 depending on origin
// (extractor vs JSON deserialisation), so accept both.
func argPositionFromMeta(m map[string]any) (int, bool) {
	return intFromMeta(m, "arg_position")
}

func intFromMeta(m map[string]any, key string) (int, bool) {
	if m == nil {
		return 0, false
	}
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	}
	return 0, false
}
