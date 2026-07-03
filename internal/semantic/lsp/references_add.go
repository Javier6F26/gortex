package lsp

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/semantic"
)

// referencesAddPass adds call edges for a references-capable server whose
// call hierarchy is absent (e.g. intelephense). For each function / method
// declaration it asks textDocument/references and mints an lsp_resolved
// EdgeCalls from each reference site's enclosing callable to the target —
// the add-phase analogue of a call-hierarchy hop for servers that implement
// only references. Without it the per-file sweep's hierarchy hops never run,
// so the enrich pass confirms existing edges but never ADDS the dispatch
// call sites the server can enumerate (the "confirm-only, edges_added 0"
// outcome). Provider-generic: any references-capable, call-hierarchy-absent
// server benefits.
//
// Targets are ordered dispatch-anchors-first (interface / trait members,
// then abstract-marked methods, then by descending fan-in) so a per-repo
// deadline is spent where recall is scarcest. Each declaration commits under
// rmu as soon as its references land, so a deadline cut loses only the
// unvisited remainder. Runs under the caller's targeted context (the same
// budget the confirm pass uses), before the per-file hover sweep.
func (p *Provider) referencesAddPass(ctx context.Context, g graph.Store, repoPrefix, absRoot string, langNodes []*graph.Node, rmu sync.Locker, result *semantic.EnrichResult) {
	targets := selectReferencesAddTargets(g, langNodes)
	if len(targets) == 0 {
		return
	}
	result.ReferencesAddPass = true

	// Site-file contents cached for the cheap name-token guard, keyed by
	// repo-relative path. Site files are read from disk, never opened on the
	// server — attribution uses the graph, not the LSP.
	siteSrc := map[string][]byte{}
	// Within one pass a (caller, target) pair maps to a single edge: the
	// first site wins its FilePath/Line, later sites are recorded on the
	// edge's call_sites so find_usages still renders one row per site (see
	// internal/graph/call_sites.go). Edges W2 minted accumulate call_sites;
	// pre-existing edges (which the AST already emits per call site) are only
	// promoted, so their per-line siblings carry the multiplicity.
	mintedEdge := map[string]*graph.Edge{}
	promoted := map[string]bool{}

	for _, n := range targets {
		if ctx.Err() != nil {
			break
		}
		line, ok := lspLine(n)
		if !ok {
			continue
		}
		rel := nodeRelPath(n)
		if !p.servesFile(rel) {
			continue // never open a file this server can't compile
		}
		if err := p.openDocument(absRoot, rel); err != nil {
			continue
		}
		col := identifierColumn(p.getSource(absRoot, rel), n.StartLine, n.Name)
		refs, err := p.findReferences(absRoot, rel, line, col)
		_ = p.closeDocument(filepath.Join(absRoot, rel))
		if err != nil || len(refs) == 0 {
			continue
		}

		rmu.Lock()
		for _, loc := range refs {
			sitePath := uriToPath(loc.URI, absRoot)
			if sitePath == "" {
				continue // reference outside the repo
			}
			siteLine := loc.Range.Start.Line + 1
			enclosing := semantic.MatchCallableByFileLine(g, scopedPath(repoPrefix, sitePath), siteLine)
			if enclosing == nil || enclosing.ID == n.ID {
				continue // unattributable, or the declaration's own identifier
			}
			key := enclosing.ID + "\x00" + n.ID
			// Cheap text guard: the site line must actually contain the
			// target's whole name token, to defuse a stale position or a
			// server bug before minting a compiler-grade edge.
			if content := siteFileContent(siteSrc, absRoot, nodeRelPath(enclosing)); content != nil {
				if _, found := identifierColumnStrict(content, siteLine, n.Name); !found {
					continue
				}
			}
			// A later site of an edge W2 already minted: record it on the edge
			// instead of minting a duplicate.
			if e0, ok := mintedEdge[key]; ok {
				graph.AppendCallSite(e0, enclosing.FilePath, siteLine)
				semantic.PersistEdge(g, e0)
				continue
			}
			// A later site of an edge the AST already produced per line — the
			// promotion already ran, and its per-line siblings carry the sites.
			if promoted[key] {
				continue
			}
			if existing := semantic.FindMatchingEdge(g, enclosing.ID, n.ID, graph.EdgeCalls); existing != nil {
				if graph.OriginRank(existing.Origin) < graph.OriginRank(graph.OriginLSPResolved) {
					semantic.ConfirmEdge(existing, p.Name())
					existing.Origin = graph.OriginLSPResolved
					semantic.PersistEdge(g, existing)
					result.EdgesConfirmed++
				}
				promoted[key] = true
				continue
			}
			mintedEdge[key] = semantic.AddSemanticEdge(g, enclosing.ID, n.ID, graph.EdgeCalls,
				enclosing.FilePath, siteLine, p.Name())
			result.EdgesAdded++
		}
		rmu.Unlock()
	}
}

// selectReferencesAddTargets returns the function / method nodes to query,
// ordered dispatch-anchors-first: interface / trait members (tier 2), then
// abstract-marked methods (tier 1), then plain methods (tier 0); within a
// tier by descending fan-in so a deadline is spent on the most-referenced
// declarations first.
func selectReferencesAddTargets(g graph.Store, langNodes []*graph.Node) []*graph.Node {
	// Interface / trait type names in the repo — a method whose receiver is
	// one of these is a dispatch anchor the reference set fans out across.
	dispatchOwners := map[string]bool{}
	for _, n := range langNodes {
		switch {
		case n.Kind == graph.KindInterface:
			dispatchOwners[n.Name] = true
		case n.Kind == graph.KindType && isTraitNode(n):
			dispatchOwners[n.Name] = true
		}
	}

	type scored struct {
		n     *graph.Node
		tier  int
		fanIn int
	}
	var cand []scored
	ids := make([]string, 0, len(langNodes))
	for _, n := range langNodes {
		if n.Kind != graph.KindFunction && n.Kind != graph.KindMethod {
			continue
		}
		tier := 0
		if recv, _ := n.Meta["receiver"].(string); recv != "" && dispatchOwners[recv] {
			tier = 2
		} else if isAbstractMarked(n) {
			tier = 1
		}
		cand = append(cand, scored{n: n, tier: tier})
		ids = append(ids, n.ID)
	}
	if len(cand) == 0 {
		return nil
	}
	// One batched store call for fan-in instead of a point lookup per node.
	inEdges := g.GetInEdgesByNodeIDs(ids)
	for i := range cand {
		cand[i].fanIn = len(inEdges[cand[i].n.ID])
	}
	sort.SliceStable(cand, func(i, j int) bool {
		if cand[i].tier != cand[j].tier {
			return cand[i].tier > cand[j].tier
		}
		return cand[i].fanIn > cand[j].fanIn
	})
	out := make([]*graph.Node, len(cand))
	for i := range cand {
		out[i] = cand[i].n
	}
	return out
}

// isTraitNode reports whether a KindType node models a trait (PHP traits are
// KindType with a trait flavor, unlike C#/Rust which use KindInterface).
func isTraitNode(n *graph.Node) bool {
	if n.Meta == nil {
		return false
	}
	if k, _ := n.Meta["kind"].(string); k == "trait" {
		return true
	}
	if tf, _ := n.Meta["type_flavor"].(string); tf == "trait" {
		return true
	}
	return false
}

// isAbstractMarked reports whether a method node is an abstract / interface /
// trait / virtual declaration, across the per-language marker keys the
// extractors stamp (php abstract, csharp iface_member, rust trait_decl,
// phpdoc virtual).
func isAbstractMarked(n *graph.Node) bool {
	if n.Meta == nil {
		return false
	}
	if b, ok := n.Meta["abstract"].(bool); ok && b {
		return true
	}
	if b, ok := n.Meta["iface_member"].(bool); ok && b {
		return true
	}
	if s, ok := n.Meta["trait_decl"].(string); ok && s == "true" {
		return true
	}
	if _, ok := n.Meta["virtual"]; ok {
		return true
	}
	return false
}

// siteFileContent reads (and caches) a repo-relative site file for the
// name-token guard. A read error caches nil so a missing file is not retried.
func siteFileContent(cache map[string][]byte, absRoot, rel string) []byte {
	if rel == "" {
		return nil
	}
	if c, ok := cache[rel]; ok {
		return c
	}
	c, err := os.ReadFile(filepath.Join(absRoot, rel))
	if err != nil {
		cache[rel] = nil
		return nil
	}
	cache[rel] = c
	return c
}
