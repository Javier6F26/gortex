package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// registerScopeTools wires the saved-scope management tools.
func (s *Server) registerScopeTools() {
	s.addTool(mcp.NewTool("save_scope",
		mcp.WithDescription("Save a named, reusable repository scope — a slice of a multi-repo "+
			"workspace. Once saved, pass scope:\"<name>\" to search_symbols or smart_context instead "+
			"of re-listing repo filters on every call. Persists across daemon restarts."),
		mcp.WithString("name", mcp.Required(), mcp.Description("Scope name")),
		mcp.WithString("repos", mcp.Required(),
			mcp.Description("Comma-separated repository prefixes the scope covers")),
		mcp.WithString("description", mcp.Description("Optional human-readable description")),
		mcp.WithString("paths", mcp.Description("Optional comma-separated sub-paths that narrow the scope below the repository grain -- the monorepo-service slice (e.g. \"services/billing,libs/auth\"). Anchored, slash-segment prefixes relative to each repo root. Omit for a repo-level scope.")),
	), s.handleSaveScope)

	s.addTool(mcp.NewTool("list_scopes",
		mcp.WithDescription("List every saved repository scope (see save_scope) with its repositories."),
	), s.handleListScopes)

	s.addTool(mcp.NewTool("delete_scope",
		mcp.WithDescription("Delete a saved repository scope by name."),
		mcp.WithString("name", mcp.Required(), mcp.Description("Scope name to delete")),
	), s.handleDeleteScope)
}

func (s *Server) handleSaveScope(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := strings.TrimSpace(req.GetString("name", ""))
	if name == "" {
		return mcp.NewToolResultError("name is required"), nil
	}
	reposArg, err := req.RequireString("repos")
	if err != nil {
		return mcp.NewToolResultError("repos is required (comma-separated repository prefixes)"), nil
	}
	var repos []string
	for _, r := range strings.Split(reposArg, ",") {
		if r = strings.TrimSpace(r); r != "" {
			repos = append(repos, r)
		}
	}
	if len(repos) == 0 {
		return mcp.NewToolResultError("repos must name at least one repository prefix"), nil
	}
	sort.Strings(repos)
	var paths []string
	for _, p := range strings.Split(req.GetString("paths", ""), ",") {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	sc := SavedScope{
		Name:        name,
		Description: strings.TrimSpace(req.GetString("description", "")),
		Repos:       repos,
		Paths:       paths,
	}
	if err := s.scopeStoreOrInit().put(sc); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to persist scope: %v", err)), nil
	}
	return s.respondJSONOrTOON(ctx, req, map[string]any{
		"status": "saved",
		"scope":  sc,
		"note":   fmt.Sprintf("pass scope:%q to search_symbols or smart_context to use it", name),
	})
}

func (s *Server) handleListScopes(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	scopes := s.scopeStoreOrInit().list()
	return s.respondJSONOrTOON(ctx, req, map[string]any{
		"total":  len(scopes),
		"scopes": scopes,
	})
}

func (s *Server) handleDeleteScope(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := strings.TrimSpace(req.GetString("name", ""))
	if name == "" {
		return mcp.NewToolResultError("name is required"), nil
	}
	removed, err := s.scopeStoreOrInit().remove(name)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to persist scope deletion: %v", err)), nil
	}
	if !removed {
		return mcp.NewToolResultError(fmt.Sprintf("no saved scope named %q", name)), nil
	}
	return s.respondJSONOrTOON(ctx, req, map[string]any{"status": "deleted", "name": name})
}
