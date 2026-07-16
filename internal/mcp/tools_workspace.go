package mcp

import (
	"context"
	"sort"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/zzet/gortex/internal/graph"
)

// These are unconditionally registered (single-project mode degrades
// to a one-member view) so an agent's first call into the server can
// discover what `repo` values are legal before issuing any
// scope: repo or scope: fan-out call.
func (s *Server) registerWorkspaceTools() {
	s.addTool(
		mcp.NewTool("list_repos",
			mcp.WithDescription(
				"Lists every project in the active workspace. Workspace-scope tool: do not pass `repo`. "+
					"In workspace mode returns the auto-discovered, non-excluded children. "+
					"In single-project mode returns the one bound project as a degenerate one-member workspace."),
			mcp.WithString("format", mcp.Description("Output format: json (default), gcx (GCX1 compact wire format), or toon")),
			mcp.WithNumber("max_bytes", mcp.Description("Cap the marshaled response at this many bytes; truncation metadata rides on the response.")),
		),
		s.handleListRepos,
	)

	s.addTool(
		mcp.NewTool("workspace_info",
			mcp.WithDescription(
				"Returns workspace identity: bind mode, root directory, marker contents, the auto-discovered member set, and any unknown marker keys. "+
					"Workspace-scope tool: do not pass `repo`."),
			mcp.WithString("format", mcp.Description("Output format: json (default), gcx (GCX1 compact wire format), or toon")),
			mcp.WithNumber("max_bytes", mcp.Description("Cap the marshaled response at this many bytes; truncation metadata rides on the response.")),
		),
		s.handleWorkspaceInfo,
	)
}

// handleListRepos implements scope: workspace's `list_repos`. Returns
// the auto-discovered, non-excluded member set. Single-project mode
// degrades to the one-member [bound project] list.
//
// Pre-handshake (Bind() == nil): returns an empty list rather than
// erroring. The MCP server may be running in legacy single-repo mode
// where no two-entry-point handshake has happened — in that case the
// concept of a workspace doesn't apply and an empty list is the
// honest answer.
func (s *Server) handleListRepos(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Enforce scope: workspace's "no `repo`" rule explicitly so a
	// caller passing `repo` gets a clear protocol error instead of
	// silent acceptance.
	if _, errResult := s.ResolveToolScope("list_repos", req.GetArguments()["repo"]); errResult != nil {
		return errResult, nil
	}

	return s.respondJSONOrTOON(ctx, req, s.buildListReposPayload(ctx))
}

// buildListReposPayload returns the same data the `list_repos` tool
// emits. Shared with the `gortex://repos` resource.
//
// A workspace-bound session (daemon socket path) reports the repos in
// its own resolved workspace; an unbound session falls back to the
// process-global bind.
func (s *Server) buildListReposPayload(ctx context.Context) map[string]any {
	if sessWS, _, bound := s.sessionScope(ctx); bound {
		repos := s.sessionWorkspaceRepos(ctx)
		names := make([]string, 0, len(repos))
		for _, r := range repos {
			names = append(names, r["name"])
		}
		return map[string]any{
			"mode":      "workspace",
			"workspace": sessWS,
			"repos":     names,
		}
	}
	// No bound workspace: a follower or unbound daemon serving a
	// multi-repo store still knows which repositories the graph holds.
	// Enumerate the repo prefixes from the store (the same set scope
	// resolution uses) instead of claiming an empty workspace, so a
	// client can discover the served repos. Keep mode "unbound" for
	// observability of the serving mode.
	return map[string]any{"mode": "unbound", "repos": s.graphRepoEntries()}
}

// graphRepoEntries lists the repositories present in the store as
// list_repos entries, sorted by name. Each entry carries the repo
// prefix name; index provenance (last-synced SHA + timestamp) is added
// per entry by enrichRepoProvenance when the writer stamped it.
func (s *Server) graphRepoEntries() []map[string]any {
	if s.graph == nil {
		return []map[string]any{}
	}
	prefixes := s.graph.RepoPrefixes()
	sort.Strings(prefixes)
	out := make([]map[string]any, 0, len(prefixes))
	for _, p := range prefixes {
		// An empty prefix is a single-repo / unprefixed store, not a
		// member of a multi-repo workspace — skip it so an unprefixed
		// daemon still reports an empty unbound list.
		if p == "" {
			continue
		}
		entry := map[string]any{"name": p}
		s.enrichRepoProvenance(entry, p)
		out = append(out, entry)
	}
	return out
}

// enrichRepoProvenance attaches the writer-stamped last-synced SHA and
// timestamp for repoPrefix onto a list_repos entry, when the backend
// records index provenance. Followers serve exactly what the writer
// stamped into repo_index_state — they never compute it (they have no
// working tree). A no-op on backends without RepoIndexStateReader (the
// in-memory graph) or when no row exists yet.
func (s *Server) enrichRepoProvenance(entry map[string]any, repoPrefix string) {
	reader, ok := s.graph.(graph.RepoIndexStateReader)
	if !ok {
		return
	}
	st, found, err := reader.GetRepoIndexState(repoPrefix)
	if err != nil || !found {
		return
	}
	if st.IndexedSHA != "" {
		entry["last_synced_sha"] = st.IndexedSHA
	}
	if st.IndexedAt > 0 {
		entry["last_synced_at"] = time.Unix(st.IndexedAt, 0).UTC().Format(time.RFC3339)
	}
	if st.Dirty {
		entry["dirty"] = true
	}
}

// handleWorkspaceInfo implements `workspace_info`. Returns enough
// detail for an agent to reason about the bind: mode, root, marker
// excludes, marker unknown keys, and the resolved member set with
// per-member paths.
func (s *Server) handleWorkspaceInfo(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if _, errResult := s.ResolveToolScope("workspace_info", req.GetArguments()["repo"]); errResult != nil {
		return errResult, nil
	}
	return s.respondJSONOrTOON(ctx, req, s.buildWorkspaceInfoPayload(ctx))
}

// buildWorkspaceInfoPayload returns the same data the `workspace_info`
// tool emits. Shared with the `gortex://workspace` resource.
//
// A workspace-bound session (daemon socket path) reports its own
// resolved workspace — the boundary the query tools enforce — instead
// of the process-global bind, which is nil on the daemon.
func (s *Server) buildWorkspaceInfoPayload(ctx context.Context) map[string]any {
	if sessWS, sessProj, bound := s.sessionScope(ctx); bound {
		return map[string]any{
			"mode":             "workspace",
			"workspace":        sessWS,
			"project":          sessProj,
			"members":          s.sessionWorkspaceRepos(ctx),
			"isolation_bounds": sessWS,
		}
	}
	// No bound workspace: a follower or unbound daemon serving a
	// multi-repo store still knows which repositories the graph holds.
	// Enumerate them from the store — the SAME source list_repos uses
	// (graphRepoEntries) — so workspace_info and list_repos agree instead
	// of workspace_info claiming an empty repo set (4.1).
	return map[string]any{
		"mode":  "unbound",
		"repos": s.graphRepoEntries(),
	}
}
