package indexer

import (
	"context"
	"encoding/json"
	"time"

	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/gitcmd"
	"github.com/zzet/gortex/internal/graph"
)

// persistRepoIndexState records the per-repo freshness provenance at the
// end of a (re)index. diskTarget is the durable store when indexing
// streams to disk; nil falls back to idx.graph. Backends without durable
// state (the in-memory graph) do not implement RepoIndexStateWriter, so
// the write is skipped — exactly like the file-mtime ledger.
func (idx *Indexer) persistRepoIndexState(diskTarget graph.Store, rootAbs, workspaceFP string, nodes, edges int) {
	target := graph.Store(idx.graph)
	if diskTarget != nil {
		target = diskTarget
	}
	w, ok := target.(graph.RepoIndexStateWriter)
	if !ok {
		return
	}
	sha, dirty := repoHeadAndDirty(rootAbs)
	vers, _ := json.Marshal(extractorVersionsSnapshot())
	st := graph.RepoIndexState{
		RepoPrefix:        idx.repoPrefix,
		IndexedSHA:        sha,
		Dirty:             dirty,
		IndexedAt:         time.Now().Unix(),
		WorkspaceFP:       workspaceFP,
		NodeCount:         nodes,
		EdgeCount:         edges,
		ExtractorVersions: string(vers),
	}
	if err := w.SetRepoIndexState(st); err != nil {
		idx.logger.Warn("persist repo index state failed",
			zap.String("repo", idx.repoPrefix), zap.Error(err))
	}
}

// repoHeadAndDirty returns the working tree's current commit SHA and
// whether it has uncommitted changes. Best-effort: a non-git directory or
// any git error yields ("", false) — freshness provenance never blocks
// indexing. Git shell-outs route through the shared concurrency limiter.
func repoHeadAndDirty(rootAbs string) (sha string, dirty bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sha, err := gitcmd.Output(ctx, rootAbs, "rev-parse", "HEAD")
	if err != nil {
		return "", false
	}
	status, err := gitcmd.Output(ctx, rootAbs, "status", "--porcelain")
	if err != nil {
		return sha, false
	}
	return sha, status != ""
}
