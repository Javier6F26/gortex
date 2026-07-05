package store_pg

import (
	"github.com/zzet/gortex/internal/graph"
)

var (
	_ graph.ReachableForwardByKinds = (*Store)(nil)
	_ graph.ClassHierarchyTraverser = (*Store)(nil)
	_ graph.FrontierExpander        = (*Store)(nil)
	_ graph.FileEditingContext      = (*Store)(nil)
	_ graph.FileSubGraphReader      = (*Store)(nil)
	_ graph.FileSubGraphCountReader = (*Store)(nil)
)

func (s *Store) ReachableForwardByKinds(seeds []string, kinds []graph.EdgeKind) map[string]bool {
	if len(seeds) == 0 {
		return nil
	}
	uniqKinds := dedupeEdgeKinds(kinds)
	covered := make(map[string]bool, len(seeds))
	frontier := make([]string, 0, len(seeds))
	for _, sd := range seeds {
		if sd == "" {
			continue
		}
		if covered[sd] {
			continue
		}
		covered[sd] = true
		frontier = append(frontier, sd)
	}
	if len(uniqKinds) == 0 {
		return covered
	}
	for len(frontier) > 0 {
		edges := s.GetOutEdgesByNodeIDs(frontier)
		var next []string
		seen := make(map[string]struct{}, len(frontier))
		for _, f := range frontier {
			for _, e := range edges[f] {
				if !kindInSlice(e.Kind, uniqKinds) {
					continue
				}
				if covered[e.To] {
					continue
				}
				covered[e.To] = true
				if _, ok := seen[e.To]; !ok {
					seen[e.To] = struct{}{}
					next = append(next, e.To)
				}
			}
		}
		frontier = next
	}
	return covered
}

func kindInSlice(k graph.EdgeKind, kinds []graph.EdgeKind) bool {
	for _, kk := range kinds {
		if k == kk {
			return true
		}
	}
	return false
}

func (s *Store) ClassHierarchyTraverse(seedID string, direction string, kinds []graph.EdgeKind, depth int) []graph.ClassHierarchyRow {
	if seedID == "" || depth <= 0 {
		return nil
	}
	uniqKinds := dedupeEdgeKinds(kinds)
	if len(uniqKinds) == 0 {
		return nil
	}

	var edgeFn func(string) []*graph.Edge
	switch direction {
	case "up":
		edgeFn = s.GetOutEdges
	case "down":
		edgeFn = s.GetInEdges
	default:
		return nil
	}

	type pathEntry struct {
		path      []string
		edgeKinds []graph.EdgeKind
		lastID    string
	}

	// allRows collects every node found at any depth.
	allRows := make(map[[2]string]graph.ClassHierarchyRow)

	// Find all seed's immediate neighbours at depth 1.
	paths := make([]pathEntry, 0)
	for _, e := range edgeFn(seedID) {
		if !kindInSlice(e.Kind, uniqKinds) {
			continue
		}
		target := e.To
		if direction == "down" {
			target = e.From
		}
		p := pathEntry{
			path:      []string{target},
			edgeKinds: []graph.EdgeKind{e.Kind},
			lastID:    target,
		}
		paths = append(paths, p)
		key := [2]string{target, string(e.Kind)}
		if _, ok := allRows[key]; !ok {
			allRows[key] = graph.ClassHierarchyRow{
				Path:      p.path,
				EdgeKinds: p.edgeKinds,
			}
		}
	}

	// Expand to requested depth, collecting at each level.
	for d := 1; d < depth; d++ {
		var next []pathEntry
		for _, p := range paths {
			for _, e := range edgeFn(p.lastID) {
				if !kindInSlice(e.Kind, uniqKinds) {
					continue
				}
				target := e.To
				if direction == "down" {
					target = e.From
				}
				cyclical := false
				for _, id := range p.path {
					if id == target {
						cyclical = true
						break
					}
				}
				if cyclical {
					continue
				}
				newPath := make([]string, len(p.path)+1)
				copy(newPath, p.path)
				newPath[len(newPath)-1] = target
				newKinds := make([]graph.EdgeKind, len(p.edgeKinds)+1)
				copy(newKinds, p.edgeKinds)
				newKinds[len(newKinds)-1] = e.Kind
				np := pathEntry{
					path:      newPath,
					edgeKinds: newKinds,
					lastID:    target,
				}
				next = append(next, np)
				key := [2]string{target, string(e.Kind)}
				if _, ok := allRows[key]; !ok {
					allRows[key] = graph.ClassHierarchyRow{
						Path:      newPath,
						EdgeKinds: newKinds,
					}
				}
			}
		}
		if len(next) == 0 {
			break
		}
		paths = next
	}

	if len(allRows) == 0 {
		return nil
	}
	out := make([]graph.ClassHierarchyRow, 0, len(allRows))
	for _, row := range allRows {
		out = append(out, row)
	}
	return out
}

func (s *Store) ExpandFrontier(ids []string, forward bool, kinds []graph.EdgeKind, limit int) []graph.FrontierHop {
	if len(ids) == 0 || len(kinds) == 0 {
		return nil
	}
	uniqKinds := dedupeEdgeKinds(kinds)
	uniq := dedupeNonEmpty(ids)

	var allEdges []*graph.Edge
	for _, id := range uniq {
		var edges []*graph.Edge
		if forward {
			edges = s.GetOutEdges(id)
		} else {
			edges = s.GetInEdges(id)
		}
		for _, e := range edges {
			if kindInSlice(e.Kind, uniqKinds) {
				allEdges = append(allEdges, e)
			}
		}
	}

	if len(allEdges) == 0 {
		return nil
	}
	if limit > 0 && len(allEdges) > limit {
		allEdges = allEdges[:limit]
	}

	neighbourIDs := make([]string, 0, len(allEdges))
	seen := make(map[string]struct{}, len(allEdges))
	for _, e := range allEdges {
		nid := e.To
		if !forward {
			nid = e.From
		}
		if _, ok := seen[nid]; !ok {
			seen[nid] = struct{}{}
			neighbourIDs = append(neighbourIDs, nid)
		}
	}

	neighbours := s.GetNodesByIDs(neighbourIDs)
	hops := make([]graph.FrontierHop, 0, len(allEdges))
	for _, e := range allEdges {
		nid := e.To
		if !forward {
			nid = e.From
		}
		n := neighbours[nid]
		if n == nil {
			continue
		}
		hops = append(hops, graph.FrontierHop{Edge: e, Neighbor: n})
	}
	return hops
}

func (s *Store) FileEditingContext(filePath string, kinds []graph.NodeKind) *graph.FileEditingContextResult {
	fileNode := s.GetNode(filePath)
	if fileNode == nil {
		return nil
	}
	defines := make([]*graph.Node, 0)
	imports := make([]*graph.Edge, 0)

	allNodes := s.GetFileNodes(filePath)
	for _, n := range allNodes {
		if n.Kind == "file" || n.Kind == "import" {
			continue
		}
		defines = append(defines, n)
	}

	for _, e := range s.GetOutEdges(filePath) {
		if e.Kind == "imports" {
			imports = append(imports, e)
		}
	}

	defineIDs := make([]string, len(defines))
	for i, n := range defines {
		defineIDs[i] = n.ID
	}

	oneHopKinds := []graph.EdgeKind{graph.EdgeCalls, graph.EdgeCrossRepoCalls}

	calledBy := make([]*graph.Node, 0)
	inEdges := s.GetInEdgesByNodeIDs(defineIDs)
	seenCB := make(map[string]bool)
	for _, edges := range inEdges {
		for _, e := range edges {
			if kindInSlice(e.Kind, oneHopKinds) {
				n := s.GetNode(e.From)
				if n != nil && n.FilePath != filePath && !seenCB[n.ID] {
					seenCB[n.ID] = true
					calledBy = append(calledBy, n)
				}
			}
		}
	}

	calls := make([]*graph.Node, 0)
	outEdges := s.GetOutEdgesByNodeIDs(defineIDs)
	seenC := make(map[string]bool)
	for _, edges := range outEdges {
		for _, e := range edges {
			if kindInSlice(e.Kind, oneHopKinds) {
				n := s.GetNode(e.To)
				if n != nil && n.FilePath != filePath && !seenC[n.ID] {
					seenC[n.ID] = true
					calls = append(calls, n)
				}
			}
		}
	}

	return &graph.FileEditingContextResult{
		FileNode: fileNode,
		Defines:  defines,
		Imports:  imports,
		CalledBy: calledBy,
		Calls:    calls,
	}
}

func (s *Store) GetFileSubGraph(filePath string) ([]*graph.Node, []*graph.Edge) {
	nodes := s.GetFileNodes(filePath)
	if len(nodes) == 0 {
		return nil, nil
	}
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	inEdges := s.GetInEdgesByNodeIDs(ids)
	outEdges := s.GetOutEdgesByNodeIDs(ids)
	edgeSet := make(map[string]*graph.Edge)
	for _, edges := range inEdges {
		for _, e := range edges {
			key := e.From + "\x00" + e.To + "\x00" + string(e.Kind)
			edgeSet[key] = e
		}
	}
	for _, edges := range outEdges {
		for _, e := range edges {
			key := e.From + "\x00" + e.To + "\x00" + string(e.Kind)
			edgeSet[key] = e
		}
	}
	edges := make([]*graph.Edge, 0, len(edgeSet))
	for _, e := range edgeSet {
		edges = append(edges, e)
	}
	return nodes, edges
}

func (s *Store) GetFileSubGraphCounts(filePath string) ([]*graph.Node, int) {
	nodes := s.GetFileNodes(filePath)
	if len(nodes) == 0 {
		return nil, 0
	}
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	edgeSet := make(map[string]struct{})
	inEdges := s.GetInEdgesByNodeIDs(ids)
	for _, edges := range inEdges {
		for _, e := range edges {
			key := e.From + "\x00" + e.To + "\x00" + string(e.Kind)
			edgeSet[key] = struct{}{}
		}
	}
	outEdges := s.GetOutEdgesByNodeIDs(ids)
	for _, edges := range outEdges {
		for _, e := range edges {
			key := e.From + "\x00" + e.To + "\x00" + string(e.Kind)
			edgeSet[key] = struct{}{}
		}
	}
	return nodes, len(edgeSet)
}
