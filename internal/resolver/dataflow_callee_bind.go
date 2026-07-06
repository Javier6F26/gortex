package resolver

import (
	"strings"

	"github.com/zzet/gortex/internal/graph"
)

// bindDataflowCalleeRefs lifts the callee side of dataflow edges
// (arg_of / value_flow) from an `unresolved::<bareName>` placeholder onto the
// same-file function or method that name denotes.
//
// The main resolver's resolveFunctionCall already binds `calls` edges to these
// callees, but it keys the candidate lookup off the edge's From node's repo —
// and a dataflow edge's From is an `unresolved::` argument placeholder with no
// node, so the lookup is scoped to the empty repo, matches nothing in a
// multi-repo graph, and the callee never lifts. materializeDataflowParams
// (which refines a resolved callee to its param node) then skips the edge
// because its target is still an `unresolved::` stub. The result was ~half of
// all arg_of edges left dangling placeholder→placeholder even when the callee
// was defined in the very same file.
//
// This pass closes that gap cheaply and without touching the hot per-edge
// resolver: it builds one file→name index of function/method nodes, then does
// an O(1) same-file lookup per dataflow edge. Same-file matching needs no repo
// derivation — an edge's FilePath and its callee node's FilePath are compared
// directly, so it behaves identically in bare (single-repo / test) and
// prefixed (multi-repo) graphs. A name with more than one same-file definition
// (a func and a method sharing a name) is left unresolved so the audit still
// surfaces it.
//
// Ordering (see runFileAttributionPassesLocked): after bindBareNameScopeRefs
// (a same-scope local/param wins over a same-file function of the same name)
// and before attributeGoBuiltins (a bare `append`/`len` argument with no
// same-file definition falls through to builtin attribution) and before
// materializeDataflowParams (which then refines the resolved callee to its
// param node).
func (r *Resolver) bindDataflowCalleeRefs() {
	byFile := map[string]map[string][]string{}
	for _, k := range []graph.NodeKind{graph.KindFunction, graph.KindMethod} {
		for n := range r.graph.NodesByKind(k) {
			if n == nil || n.Name == "" || n.FilePath == "" {
				continue
			}
			indexCalleeNode(byFile, n.FilePath, n.Name, n.ID)
		}
	}
	if len(byFile) == 0 {
		return
	}
	var batch []graph.EdgeReindex
	for _, ek := range []graph.EdgeKind{graph.EdgeArgOf, graph.EdgeValueFlow} {
		for e := range r.graph.EdgesByKind(ek) {
			if old := bindDataflowCalleeEdge(e, byFile); old != "" {
				batch = append(batch, graph.EdgeReindex{Edge: e, OldTo: old})
			}
		}
	}
	if len(batch) > 0 {
		r.graph.ReindexEdges(batch)
	}
}

// bindDataflowCalleeRefsForFile is the single-file scope of
// bindDataflowCalleeRefs, used on the incremental (fsnotify / edit_file)
// re-index path. Because the binding is same-file only, the index is built
// from this file's own function/method nodes and only this file's outgoing
// arg_of / value_flow edges are considered — producing exactly the same binds
// as the whole-graph sweep for the file (keeping
// TestIncrementalReindex_ConvergesToFullIndex green) without scanning every
// function in the graph.
func (r *Resolver) bindDataflowCalleeRefsForFile(filePath string) {
	byFile := map[string]map[string][]string{}
	for _, n := range r.graph.GetFileNodes(filePath) {
		if n == nil || (n.Kind != graph.KindFunction && n.Kind != graph.KindMethod) {
			continue
		}
		if n.Name == "" || n.FilePath == "" {
			continue
		}
		indexCalleeNode(byFile, n.FilePath, n.Name, n.ID)
	}
	if len(byFile) == 0 {
		return
	}
	var batch []graph.EdgeReindex
	for _, e := range r.fileOutEdges(filePath) {
		if e.Kind != graph.EdgeArgOf && e.Kind != graph.EdgeValueFlow {
			continue
		}
		if old := bindDataflowCalleeEdge(e, byFile); old != "" {
			batch = append(batch, graph.EdgeReindex{Edge: e, OldTo: old})
		}
	}
	if len(batch) > 0 {
		r.graph.ReindexEdges(batch)
	}
}

// indexCalleeNode records a function/method node under its file and name.
func indexCalleeNode(byFile map[string]map[string][]string, filePath, name, id string) {
	names := byFile[filePath]
	if names == nil {
		names = map[string][]string{}
		byFile[filePath] = names
	}
	names[name] = append(names[name], id)
}

// bindDataflowCalleeEdge rewrites e.To from `unresolved::<bareName>` to the
// sole same-file function/method of that name. Returns the old To value when a
// rewrite happened (for the batched reindex) or "" when the edge was left
// alone (not a bare unresolved target, no same-file definition, or ambiguous).
func bindDataflowCalleeEdge(e *graph.Edge, byFile map[string]map[string][]string) string {
	if e == nil || !graph.IsUnresolvedTarget(e.To) {
		return ""
	}
	name := graph.UnresolvedName(e.To)
	// Bare identifier only — selector (*.m), qualified (a::b), extern, and
	// per-binding (#...) shapes are owned by other passes.
	if name == "" || strings.ContainsAny(name, ".*:#") {
		return ""
	}
	ids := byFile[e.FilePath][name]
	if len(ids) != 1 {
		return ""
	}
	if ids[0] == e.To {
		return ""
	}
	oldTo := e.To
	e.To = ids[0]
	return oldTo
}
