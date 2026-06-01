package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/zzet/gortex/internal/churn"
	"github.com/zzet/gortex/internal/releases"
)

// registerEnrichReleasesTool exposes the releases enricher as an MCP
// tool. `analyze kind=releases` is now a pure read — populating the
// per-file meta.added_in and the KindRelease timeline is this tool's
// job (counterpart to enrich_churn).
//
// Branch constrains the considered tags to those reachable from the
// branch — typically the repo's default branch — so topic-branch tags
// don't pollute the timeline. Empty branch means "every tag", matching
// the legacy behaviour.
func (s *Server) registerEnrichReleasesTool() {
	s.addTool(
		mcp.NewTool("enrich_releases",
			mcp.WithDescription("Pre-compute the release timeline: list tags on the default branch (or `branch` override), stamp meta.added_in on every file present in each tag's tree, and materialise one KindRelease node per tag. The read tool `analyze kind=releases` then answers from this Meta without re-walking git. Idempotent; disk-backed daemons (sqlite) persist the result across restarts."),
			mcp.WithString("branch", mcp.Description("Branch / tag / SHA whose reachable tag set bounds the timeline. Empty resolves the repo's default branch; pass a value to override.")),
			mcp.WithString("path", mcp.Description("Optional path or repo prefix to scope the enrichment. Multi-repo daemons enrich every tracked repo when empty.")),
			mcp.WithString("format", mcp.Description("Output format: json (default), gcx, or toon")),
		),
		s.handleEnrichReleases,
	)
}

func (s *Server) handleEnrichReleases(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if s.graph == nil {
		return mcp.NewToolResultError("graph not initialized"), nil
	}
	branchArg := strings.TrimSpace(req.GetString("branch", ""))
	pathArg := strings.TrimSpace(req.GetString("path", ""))

	type target struct {
		prefix string
		root   string
	}
	var targets []target
	if s.multiIndexer != nil {
		for prefix, meta := range s.multiIndexer.AllMetadata() {
			if pathArg != "" && pathArg != prefix && pathArg != meta.RootPath {
				continue
			}
			targets = append(targets, target{prefix: prefix, root: meta.RootPath})
		}
	}
	if len(targets) == 0 {
		return mcp.NewToolResultError(fmt.Sprintf("no tracked repo matches %q", pathArg)), nil
	}
	_ = ctx

	started := time.Now()
	type perRepo struct {
		Prefix  string `json:"prefix"`
		Branch  string `json:"branch,omitempty"`
		Files   int    `json:"files"`
		Skipped string `json:"skipped,omitempty"`
	}
	var per []perRepo
	totalFiles := 0
	for _, t := range targets {
		b := branchArg
		if b == "" {
			b = churn.DefaultBranch(t.root)
			// b can stay "" — releases.EnrichGraphForBranch treats
			// that as "every tag", the right fallback when no default
			// branch resolves.
		}
		count, err := releases.EnrichGraphForBranch(s.graph, t.root, t.prefix, b)
		if err != nil {
			per = append(per, perRepo{Prefix: t.prefix, Branch: b, Skipped: err.Error()})
			continue
		}
		per = append(per, perRepo{Prefix: t.prefix, Branch: b, Files: count})
		totalFiles += count
	}

	return s.respondJSONOrTOON(ctx, req, map[string]any{
		"repos":       per,
		"files":       totalFiles,
		"duration_ms": time.Since(started).Milliseconds(),
	})
}
