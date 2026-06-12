package indexer

import (
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// persistConstValues writes a file's extracted constant literal values to
// the backend's constant_values sidecar (when it implements
// graph.ConstantValueWriter — the on-disk backend and the in-memory
// store both do). The resolver reads these to dereference a
// const-identifier Temporal dispatch name to its literal value across
// files.
//
// ExtractionResult.ConstValues carries pre-repo-prefix node ids / file
// paths (they are stamped at extraction time, before applyRepoPrefix
// rewrites the node ids). This helper replicates that same prefix
// transform so the persisted node_id matches the final graph node id the
// resolver looks up by, independent of when applyRepoPrefix ran. Each
// file's prior rows are deleted first so a reindex replaces them cleanly.
func (idx *Indexer) persistConstValues(result *parser.ExtractionResult) {
	if result == nil || len(result.ConstValues) == 0 {
		return
	}
	cw, ok := idx.graph.(graph.ConstantValueWriter)
	if !ok {
		return
	}
	prefix := ""
	if idx.repoPrefix != "" {
		prefix = idx.repoPrefix + "/"
	}
	rows := make([]graph.ConstantValueRow, 0, len(result.ConstValues))
	fileSet := map[string]struct{}{}
	for _, cv := range result.ConstValues {
		rows = append(rows, graph.ConstantValueRow{
			NodeID:   prefix + cv.NodeID,
			FilePath: prefix + cv.FilePath,
			Value:    cv.Value,
		})
		fileSet[prefix+cv.FilePath] = struct{}{}
	}
	files := make([]string, 0, len(fileSet))
	for f := range fileSet {
		files = append(files, f)
	}
	_ = cw.DeleteConstantValuesByFiles(idx.repoPrefix, files)
	_ = cw.BulkSetConstantValues(idx.repoPrefix, rows)
}
