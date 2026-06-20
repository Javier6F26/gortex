package indexer

import (
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/artifacts"
	"github.com/zzet/gortex/internal/graph"
)

// contentLinkMinEdgeBudget is the floor on EdgeMotivates the doc->code linker
// may add. Above it the budget scales to 10% of the live edge count, so the
// derived why-layer can never approach doubling the graph.
const contentLinkMinEdgeBudget = 2000

// contentLinkEdgeBudget is the single authority bounding how many EdgeMotivates
// edges the content->code linker emits: max(2000, 10% of live edges).
func contentLinkEdgeBudget(edgeCount int) int {
	if scaled := edgeCount / 10; scaled > contentLinkMinEdgeBudget {
		return scaled
	}
	return contentLinkMinEdgeBudget
}

// linkContentToCode mints EdgeMotivates edges from content chunks (pdf / office
// / text KindDoc tagged data_class=content) to the code symbols their text
// names, reusing the artifact reference scanner (whole-token match, 4-char
// floor, 200 refs/chunk, 1 MiB scan cap). Same-repo edges are emitted here;
// because EdgeMotivates is registered in BaseKindsForCrossRepo, the
// DetectCrossRepoEdges pass that runs next mints the cross_repo_motivates
// parallel for any edge spanning a repo boundary. Bounded by a single edge
// budget so the why-layer cannot bloat the graph; over budget, it stops and
// logs rather than silently truncating.
func (idx *Indexer) linkContentToCode() {
	g := idx.graph
	if g == nil {
		return
	}
	nameIndex := artifacts.SymbolNameIndex(g, "") // union across all repos
	if len(nameIndex) == 0 {
		return
	}
	budget := contentLinkEdgeBudget(g.EdgeCount())

	var edges []*graph.Edge
	added := 0
	truncated := false
	for _, chunk := range g.AllNodes() {
		if chunk == nil || chunk.Kind != graph.KindDoc || chunk.Meta == nil {
			continue
		}
		if dc, _ := chunk.Meta["data_class"].(string); dc != "content" {
			continue
		}
		text, _ := chunk.Meta["section_text"].(string)
		if text == "" {
			continue
		}
		for _, sym := range artifacts.ScanSymbolRefs([]byte(text), nameIndex) {
			if sym == chunk.ID {
				continue
			}
			if added >= budget {
				truncated = true
				break
			}
			edges = append(edges, &graph.Edge{
				From: chunk.ID, To: sym, Kind: graph.EdgeMotivates,
				FilePath: chunk.FilePath, Origin: graph.OriginTextMatched,
				Meta: map[string]any{"signal": "lexical"},
			})
			added++
		}
		if truncated {
			break
		}
	}
	if len(edges) > 0 {
		g.AddBatch(nil, edges)
	}
	if idx.logger == nil {
		return
	}
	if truncated {
		idx.logger.Warn("indexer: content->code linking hit the edge budget; remaining links dropped",
			zap.Int("budget", budget), zap.Int("emitted", added))
	} else if added > 0 {
		idx.logger.Info("indexer: content->code why-links emitted (global)", zap.Int("edges", added))
	}
}
