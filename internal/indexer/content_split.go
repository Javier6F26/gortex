package indexer

import (
	"unicode/utf8"

	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/graph"
)

// contentSnippetCap bounds how much of a CONTENT (data_class="content")
// section body is retained on the graph node after its full text has been
// streamed into the dedicated content index. The full body lives in the
// content FTS; the node keeps only this much for display (the why-layer,
// get_symbol_source) — so a repo of hundreds of thousands of sections
// holds ~240 B × N in the graph instead of ~4 KB × N (~17× less text).
const contentSnippetCap = 240

// isContentNode is the indexer-local alias for graph.IsContentNode — the
// shared predicate for a CONTENT section node (KindDoc, data_class=content).
func isContentNode(n *graph.Node) bool {
	return graph.IsContentNode(n)
}

// contentBody returns the full section text carried on a content node, or
// "" if absent.
func contentBody(n *graph.Node) string {
	if n.Meta == nil {
		return ""
	}
	b, _ := n.Meta["section_text"].(string)
	return b
}

// metaInt reads an integer-valued Meta key, tolerating the int / int64 /
// float64 forms a value can take across a gob round-trip.
func metaInt(meta map[string]any, key string) int {
	switch v := meta[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

// contentOrdinal returns the section's ordinal — "ordinal" for text /
// office chunks, "page" for PDF pages.
func contentOrdinal(n *graph.Node) int {
	if o := metaInt(n.Meta, "ordinal"); o != 0 {
		return o
	}
	return metaInt(n.Meta, "page")
}

// safeTruncate returns s clamped to at most maxBytes, never splitting a
// multi-byte rune.
func safeTruncate(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// leanContentNode strips a content node's full section body down to a
// capped snippet once the full text has been captured for the content
// index. The key stays "section_text" so display consumers keep working;
// only the length changes. "content_indexed" marks that the full body
// lives in the content index (so a reader knows the snippet is partial).
func leanContentNode(n *graph.Node) {
	body := contentBody(n)
	if len(body) <= contentSnippetCap {
		return
	}
	n.Meta["section_text"] = safeTruncate(body, contentSnippetCap)
	n.Meta["content_indexed"] = true
}

// collectContentItems pulls one ContentFTSItem per content section node in
// the batch, carrying the FULL body for the content index. Returns nil
// when the batch has no content.
func collectContentItems(nodes []*graph.Node) []graph.ContentFTSItem {
	var items []graph.ContentFTSItem
	for _, n := range nodes {
		if !isContentNode(n) {
			continue
		}
		body := contentBody(n)
		if body == "" {
			continue
		}
		items = append(items, graph.ContentFTSItem{
			NodeID:   n.ID,
			FilePath: n.FilePath,
			Ordinal:  contentOrdinal(n),
			Body:     body,
		})
	}
	return items
}

// firstContentFilePath returns the FilePath of the first content node in
// the batch (all of one file's content nodes share it), or "" if none. The
// incremental reindex path uses it to wipe a single file's content rows
// before re-streaming.
func firstContentFilePath(nodes []*graph.Node) string {
	for _, n := range nodes {
		if isContentNode(n) {
			return n.FilePath
		}
	}
	return ""
}

// contentSearcher returns the ContentSearcher this index writes content
// section bodies to: the disk sink captured at the shadow swap (so content
// reaches disk even while idx.graph is the in-memory shadow), else
// idx.graph itself when it is a disk store. Returns nil for an in-memory
// store with no content index — in which case content text is left on the
// nodes and falls back to the symbol search (the small-repo / CLI case).
func (idx *Indexer) contentSearcher() graph.ContentSearcher {
	if idx.contentSink != nil {
		return idx.contentSink
	}
	if cs, ok := idx.graph.(graph.ContentSearcher); ok {
		return cs
	}
	return nil
}

// streamContentSections is the per-file content path: it streams a parsed
// file's content section bodies into the dedicated content index, then
// leans the nodes to a snippet — so the bulk text never enters the graph,
// the symbol search, or the materialising code passes. Called in the parse
// worker before AddBatch, so the nodes added to the graph are already
// lean. A nil content searcher (in-memory store) leaves the nodes full.
func (idx *Indexer) streamContentSections(nodes []*graph.Node) {
	cs := idx.contentSearcher()
	if cs == nil {
		return
	}
	items := collectContentItems(nodes)
	if len(items) == 0 {
		return
	}
	// Log a warning when content bodies contain non-UTF-8 bytes.
	for _, it := range items {
		if !utf8.ValidString(it.Body) {
			idx.logger.Warn("indexer: content body has non-UTF-8 bytes; will be sanitized",
				zap.String("file", it.FilePath),
				zap.String("node", it.NodeID))
			break
		}
	}
	if err := cs.AppendContent(idx.RepoPrefix(), items); err != nil {
		// Keep the full body on the nodes if the append failed, so content
		// stays searchable via the symbol-search fallback rather than lost.
		idx.logger.Warn("indexer: content index append failed; leaving section text on nodes",
			zap.Error(err))
		return
	}
	for _, n := range nodes {
		if isContentNode(n) {
			leanContentNode(n)
		}
	}
}
