package indexer

import (
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/indexer/merkle"
)

// merkleTreeFile is where the per-repo Merkle tree is persisted —
// alongside the parser quarantine, under the repo's .gortex directory.
func merkleTreeFile(rootAbs string) string {
	return filepath.Join(rootAbs, ".gortex", "merkle.json")
}

// merkleEnabled reports whether incremental re-index should detect
// changes with a BLAKE3 Merkle tree instead of per-file mtime
// comparison. GORTEX_MERKLE overrides the index.merkle config key.
func (idx *Indexer) merkleEnabled() bool {
	if v := os.Getenv("GORTEX_MERKLE"); v != "" {
		return v == "1" || strings.EqualFold(v, "true")
	}
	return idx.config.Merkle
}

// merkleStaleFiles returns the absolute paths of files whose content
// changed since the last pass. It rebuilds the Merkle tree (reusing
// hashes for mtime-unchanged files, so only changed files are re-read),
// diffs it against the persisted tree, persists the new tree, and maps
// the changed repo-relative paths back to absolute paths. A file
// touched without a content change is not reported, unlike the
// bare-mtime path.
func (idx *Indexer) merkleStaleFiles(rootAbs string, diskFiles map[string]bool) []string {
	rels := make([]string, 0, len(diskFiles))
	for rel := range diskFiles {
		rels = append(rels, rel)
	}
	treePath := merkleTreeFile(rootAbs)
	prior, _ := merkle.Load(treePath)
	tree := merkle.Build(rootAbs, rels, prior, merkleSaltFor)
	changed, _ := tree.Diff(prior)
	if err := tree.Save(treePath); err != nil {
		idx.logger.Warn("indexer: merkle tree save failed", zap.Error(err))
	}
	stale := make([]string, 0, len(changed))
	for _, rel := range changed {
		stale = append(stale, filepath.Join(rootAbs, filepath.FromSlash(rel)))
	}
	return stale
}

// saveMerkleBaseline builds and persists the Merkle tree after a full
// index, so the next incremental pass diffs against a content-addressed
// baseline rather than treating every file as changed. It returns the
// tree root — the workspace fingerprint recorded in repo_index_state and
// used to short-circuit the global derivation passes when nothing moved.
func (idx *Indexer) saveMerkleBaseline(rootAbs string, absFiles []string) string {
	rels := make([]string, 0, len(absFiles))
	for _, f := range absFiles {
		if rel, err := filepath.Rel(rootAbs, f); err == nil {
			rels = append(rels, filepath.ToSlash(rel))
		}
	}
	tree := merkle.Build(rootAbs, rels, nil, merkleSaltFor)
	if err := tree.Save(merkleTreeFile(rootAbs)); err != nil {
		idx.logger.Warn("indexer: merkle baseline save failed", zap.Error(err))
	}
	return tree.Root
}
