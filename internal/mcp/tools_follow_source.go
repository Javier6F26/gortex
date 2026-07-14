package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/zzet/gortex/internal/graph"
)

// storeKeysForRead resolves the (repoPrefix, graphPath) store keys for a
// file from any indexed node in it, so a diskless read can look up the blob
// / doc nodes. ok is false when the graph knows no nodes for relPath.
func (s *Server) storeKeysForRead(ctx context.Context, relPath string) (repoPrefix, graphPath string, ok bool) {
	sg := s.engineFor(ctx).GetFileSymbols(relPath)
	if sg == nil {
		return "", "", false
	}
	for _, n := range sg.Nodes {
		if n != nil && n.FilePath != "" {
			return n.RepoPrefix, n.FilePath, true
		}
	}
	return "", "", false
}

// This file implements the store-backed source-read fallback for
// diskless followers (see the store-backed-doc-reads and code-source-blobs
// capabilities). When disk is unavailable — a follower has no working
// tree, or a file was deleted after indexing on a normal daemon — source
// reads fall back to the graph store: byte-exact file blobs for code,
// reconstructed section text for documents.

// followNoDiskError builds the typed, recoverable error returned when a
// source read cannot be satisfied without disk (no blob, no doc nodes) or
// a git-dependent tool runs on a follower. The `follow_no_disk:` prefix is
// the machine-readable condition; the prose names the remedy.
func followNoDiskError(what string) *mcp.CallToolResult {
	return mcp.NewToolResultError(fmt.Sprintf(
		"follow_no_disk: %s is unavailable without a working tree. "+
			"Re-run the writer to populate the store, or run this against a local checkout of the repo.",
		what))
}

// sliceLines returns the 1-based inclusive [start,end] line range of body.
// Out-of-range bounds are clamped. A zero start/end returns the whole body.
func sliceLines(body string, start, end int) string {
	if start <= 0 && end <= 0 {
		return body
	}
	lines := strings.Split(body, "\n")
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}
	if start > len(lines) {
		return ""
	}
	return strings.Join(lines[start-1:end], "\n")
}

// storeSourceForNode reconstructs a single node's source from the store when
// disk is unavailable. For a KindDoc node it serves the node's own stored
// text (content body from the content index for content-class nodes; the
// meta section_text otherwise). For a code node it slices the file blob to
// the node's line range (byte-exact). Returns the source and whether it was
// served.
func (s *Server) storeSourceForNode(node *graph.Node) (string, bool) {
	if node == nil || s.graph == nil {
		return "", false
	}
	if node.Kind == graph.KindDoc {
		if graph.IsContentNode(node) {
			if cr, ok := s.graph.(graph.ContentByFileReader); ok {
				if items, err := cr.ContentByFile(node.RepoPrefix, node.FilePath); err == nil {
					for _, it := range items {
						if it.NodeID == node.ID {
							return it.Body, true
						}
					}
				}
			}
		}
		if node.Meta != nil {
			if txt, ok := node.Meta["section_text"].(string); ok && txt != "" {
				return txt, true
			}
		}
	}
	// Code (or any) node: slice the byte-exact blob to the node's range.
	if br, ok := s.graph.(graph.FileBlobReader); ok {
		if blob, found := br.GetFileBlobByPath(node.RepoPrefix, node.FilePath); found {
			return sliceLines(string(blob.Body), node.StartLine, node.EndLine), true
		}
	}
	return "", false
}

// sourceLinesForNode is the single node-anchored source-read seam
// (code-source-blobs D7): overlay → disk → store. It returns the node's
// [StartLine..EndLine] source (± contextLines), the first line number, the
// file's total size in chars (for savings estimation), and whether the bytes
// were served from the store (byte-exact blob for code, stored section text
// for docs) rather than disk/overlay. readLinesForCtx already honours an
// active editor overlay, so an unsaved buffer wins over both disk and store;
// the store is reached only when disk is unavailable — a diskless follower,
// or a file deleted after indexing on a normal daemon. Consumers
// (get_symbol_source, batch_symbols, smart_context excerpts, the pattern/
// example lookups) route through this so the disk→store fallback is
// inherited in one place instead of re-implemented per tool.
func (s *Server) sourceLinesForNode(ctx context.Context, node *graph.Node, contextLines int) (source string, fromLine, totalChars int, fromStore bool, err error) {
	if node == nil {
		return "", 0, 0, false, fmt.Errorf("nil node")
	}
	if abs, rerr := s.resolveNodePath(node); rerr == nil {
		if src, from, total, derr := s.readLinesForCtx(ctx, abs, node.StartLine, node.EndLine, contextLines); derr == nil {
			return src, from, total, false, nil
		}
	}
	if src, ok := s.storeSourceForNode(node); ok {
		return src, node.StartLine, len(src), true, nil
	}
	return "", 0, 0, false, fmt.Errorf("no source available for node %q", node.ID)
}

// storeEtagForNode returns the content-hash etag for a node's file blob when
// one is stored — the stable etag for a store-served source read.
func (s *Server) storeEtagForNode(node *graph.Node) (string, bool) {
	if node == nil || s.graph == nil {
		return "", false
	}
	if br, ok := s.graph.(graph.FileBlobReader); ok {
		if blob, found := br.GetFileBlobByPath(node.RepoPrefix, node.FilePath); found {
			return blob.ContentHash, true
		}
	}
	return "", false
}

// storeReconstructFile rebuilds a whole file's content from the store when
// disk is unavailable. It prefers the byte-exact blob (covers code and any
// file the writer stored); otherwise it reconstructs a document from its
// graph nodes: content-class sections by ordinal via ContentByFile,
// markdown sections from each node's section_text joined in line order.
// Returns (content, isDoc, ok): isDoc marks a section-level (non-byte-exact)
// reconstruction so the caller can annotate fidelity.
func (s *Server) storeReconstructFile(repoPrefix, graphPath string) ([]byte, bool, bool) {
	if s.graph == nil {
		return nil, false, false
	}
	// Byte-exact blob first.
	if br, ok := s.graph.(graph.FileBlobReader); ok {
		if blob, found := br.GetFileBlobByPath(repoPrefix, graphPath); found {
			return blob.Body, false, true
		}
	}
	// Document reconstruction from graph nodes.
	nodes := s.graph.GetFileNodes(graphPath)
	var docNodes []*graph.Node
	contentClass := false
	for _, n := range nodes {
		if n == nil || n.Kind != graph.KindDoc {
			continue
		}
		docNodes = append(docNodes, n)
		if graph.IsContentNode(n) {
			contentClass = true
		}
	}
	if len(docNodes) == 0 {
		return nil, false, false
	}
	if contentClass {
		// Ordinal-ordered content bodies from the content index.
		if cr, ok := s.graph.(graph.ContentByFileReader); ok {
			if items, err := cr.ContentByFile(repoPrefix, graphPath); err == nil && len(items) > 0 {
				parts := make([]string, 0, len(items))
				for _, it := range items {
					parts = append(parts, it.Body)
				}
				return []byte(strings.Join(parts, "\n")), true, true
			}
		}
	}
	// Markdown prose: section_text joined in start-line order.
	sort.SliceStable(docNodes, func(i, j int) bool {
		return docNodes[i].StartLine < docNodes[j].StartLine
	})
	parts := make([]string, 0, len(docNodes))
	for _, n := range docNodes {
		if n.Meta == nil {
			continue
		}
		if txt, ok := n.Meta["section_text"].(string); ok && txt != "" {
			parts = append(parts, txt)
		}
	}
	if len(parts) == 0 {
		return nil, false, false
	}
	return []byte(strings.Join(parts, "\n\n")), true, true
}
