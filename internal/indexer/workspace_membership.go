package indexer

import (
	"path/filepath"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/modules"
)

// extractPackageWorkspace detects whether this repo's root is a
// package-manager workspace (npm/yarn, pnpm, or Cargo), resolves its
// member packages, and materialises a synthetic workspace-root node
// plus one EdgePackageWorkspaceMember edge per member.
//
// A package-manager workspace is the root-manifest-owns-member-packages
// relationship — distinct from Node.WorkspaceID (Gortex's repository
// grouping) and from cross-repo edges. A repo that is not a workspace
// root produces no nodes or edges, so this pass is a no-op for the
// common single-package repo.
//
// Run from extractExternalModules, after the per-manifest module
// extraction, so member manifest file nodes the module pass may have
// created already exist; the synthetic member file nodes created here
// are idempotent on ID and harmless when a real file node is present.
func (idx *Indexer) extractPackageWorkspace() {
	if !idx.config.Coverage.IsEnabled("modules") {
		return
	}
	manifest := modules.DetectWorkspace(idx.rootPath)
	if manifest == nil {
		return
	}
	members := modules.ResolveWorkspaceMembers(idx.rootPath, manifest)
	root, edges := modules.BuildWorkspaceArtifacts(manifest, members)
	if root == nil || len(edges) == 0 {
		return
	}
	// Each member's manifest file is the edge's To endpoint. The
	// language pipeline indexes package.json / Cargo.toml, but a
	// member directory excluded from the indexed file set (or skipped
	// by an extractor) would leave the edge dangling after
	// applyRepoPrefix runs. Mint an idempotent synthetic file node per
	// member so the membership edge always has a real endpoint —
	// graph.AddBatch dedups on ID, so a real file node already in the
	// graph wins and the synthetic one is a no-op.
	nodes := make([]*graph.Node, 0, len(edges)+1)
	nodes = append(nodes, root)
	for _, mem := range members {
		nodes = append(nodes, &graph.Node{
			ID:       mem.ManifestPath,
			Kind:     graph.KindFile,
			Name:     filepath.Base(mem.ManifestPath),
			FilePath: mem.ManifestPath,
			Language: manifestLanguage(mem.ManifestPath),
		})
	}
	idx.applyRepoPrefix(nodes, edges)
	idx.graph.AddBatch(nodes, edges)
}
