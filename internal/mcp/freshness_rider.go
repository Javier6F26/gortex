package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/indexer"
)

// freshnessRiderFor returns a small structured freshness block for a
// file-reading tool whose target file has changed on disk since it was
// indexed — so an agent reading a just-edited file sees, inline, that the
// graph view may lag the working tree. It returns nil (no rider, zero
// extra tokens) for the overwhelmingly common fresh case, for non-file
// tools, and in multi-repo mode (where the legacy single-indexer's
// staleness signal is all-noise — per-repo watchers own freshness there).
//
// The check is O(1): one map lookup + one stat on the single file the
// tool targets, only for the handful of read/source tools.
func (s *Server) freshnessRiderFor(toolName string, req mcp.CallToolRequest) map[string]any {
	if s.indexer == nil || s.multiIndexer != nil {
		return nil
	}
	if os.Getenv("GORTEX_NO_FRESHNESS_RIDER") == "1" {
		return nil
	}
	rel := targetRepoRelFile(toolName, req, s.indexer.RepoPrefix())
	if rel == "" {
		return nil
	}
	stale := s.indexer.IsTrackedStale(rel)
	mismatch := s.detectWorktreeMismatch()

	// Extractor-version staleness: which languages' extractors this binary
	// bumped since the repo was indexed. Fetched alongside the index state so a
	// graph indexed by an older extractor is flagged even when the touched
	// file's content is unchanged — per-language, so the advisory names the
	// exact languages to reindex rather than implying a full cold rebuild.
	var indexState graph.RepoIndexState
	var haveState bool
	var staleLangs []string
	if r, ok := graph.Store(s.graph).(graph.RepoIndexStateReader); ok {
		if st, found, _ := r.GetRepoIndexState(s.indexer.RepoPrefix()); found {
			indexState, haveState = st, true
			staleLangs = indexer.ExtractorVersionStaleLangs(st.ExtractorVersions)
		}
	}

	if !stale && !mismatch && len(staleLangs) == 0 {
		return nil
	}
	out := map[string]any{"file": rel}
	if stale {
		out["stale"] = true
		out["hint"] = "this file changed on disk since it was last indexed; the graph view may lag the working tree"
		if haveState {
			if indexState.IndexedSHA != "" {
				out["indexed_sha"] = shortFreshSHA(indexState.IndexedSHA)
			}
			if indexState.Dirty {
				out["working_tree_dirty_at_index"] = true
			}
		}
	}
	if len(staleLangs) > 0 {
		out["extractor_stale_langs"] = staleLangs
		if fileLang := indexer.ExtractorLangForFile(rel); fileLang != "" && slices.Contains(staleLangs, fileLang) {
			out["extractor_stale_hint"] = "this file's language extractor was upgraded since indexing; reindex to pick up the newer extraction (gortex index .)"
		}
	}
	if mismatch {
		out["worktree_mismatch"] = true
		out["worktree_hint"] = "the working directory is a linked git worktree the indexed graph does not cover — results reflect another checkout"
	}
	return out
}

// detectWorktreeMismatch reports (once per server, cached) whether the
// current working directory is a linked git worktree that the indexed graph
// does not cover — i.e. the agent is working in a worktree but the graph
// reflects a different checkout, so its results may not match the files on
// disk. Single-repo only; multi-repo routing owns its own worktree scoping.
func (s *Server) detectWorktreeMismatch() bool {
	s.worktreeMismatchOnce.Do(func() {
		if s.indexer == nil || s.multiIndexer != nil {
			return
		}
		cwd, err := os.Getwd()
		if err != nil || cwd == "" {
			return
		}
		if !indexer.ResolveWorktree(cwd).IsWorktree {
			return // not a linked worktree — nothing to warn about
		}
		root := s.indexer.RootPath()
		if root == "" {
			return
		}
		// The cwd is a linked worktree. If the indexed root does not contain
		// it, the graph reflects another checkout.
		cwdResolved, _ := filepath.EvalSymlinks(cwd)
		rootResolved, _ := filepath.EvalSymlinks(root)
		if cwdResolved == "" {
			cwdResolved = cwd
		}
		if rootResolved == "" {
			rootResolved = root
		}
		if !pathWithin(cwdResolved, rootResolved) {
			s.worktreeMismatch = true
		}
	})
	return s.worktreeMismatch
}

// pathWithin reports whether child is equal to or nested under parent, on a
// slash-segment boundary (so /a/bc is not "within" /a/b).
func pathWithin(child, parent string) bool {
	child = filepath.Clean(child)
	parent = filepath.Clean(parent)
	if child == parent {
		return true
	}
	return strings.HasPrefix(child, parent+string(filepath.Separator))
}

// targetRepoRelFile extracts the repo-relative path of the single file a
// read tool targets, or "" when the tool is not file-scoped. A leading
// repo prefix is stripped so the result matches the indexer's mtime keys;
// a path that does not match is simply reported not-stale (IsTrackedStale
// returns false for an unknown key), so imperfect normalization is safe.
func targetRepoRelFile(toolName string, req mcp.CallToolRequest, prefix string) string {
	var raw string
	switch toolName {
	case "read_file", "get_file_summary", "get_editing_context":
		raw = req.GetString("path", "")
	case "get_symbol_source", "get_symbol":
		id := req.GetString("id", "")
		if i := strings.Index(id, "::"); i >= 0 {
			raw = id[:i]
		}
	default:
		return ""
	}
	if raw == "" {
		return ""
	}
	raw = filepath.ToSlash(raw)
	if prefix != "" {
		raw = strings.TrimPrefix(raw, prefix+"/")
	}
	return raw
}

// decorateResultWithFreshness attaches the freshness rider to a JSON-object
// tool response under the "freshness" key. Non-JSON-object payloads
// (GCX / TOON / arrays) are left untouched — a best-effort hint must never
// reshape a compact wire format the caller opted into.
func decorateResultWithFreshness(res *mcp.CallToolResult, rider map[string]any) *mcp.CallToolResult {
	if len(rider) == 0 {
		return res
	}
	text, ok := singleTextContent(res)
	if !ok || text == "" {
		return res
	}
	var asObj map[string]any
	if json.Unmarshal([]byte(text), &asObj) != nil {
		return res
	}
	if _, exists := asObj["freshness"]; exists {
		return res
	}
	asObj["freshness"] = rider
	body, err := json.Marshal(asObj)
	if err != nil {
		return res
	}
	return rebuildTextResult(res, string(body))
}

// shortFreshSHA trims a git SHA to 12 chars for the rider.
func shortFreshSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
