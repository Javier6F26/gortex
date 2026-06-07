package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
)

// resolveSymbolID normalizes a possibly repo-relative symbol id to its
// canonical graph id. A full id that already names a node is returned
// unchanged (exact match first, so a valid id is never reinterpreted).
// Otherwise, when the calling session's working directory maps to a
// tracked repo, the repo prefix is prepended and tried — so a caller
// inside a repo can pass repo-relative ids (internal/x.go::Foo) instead of
// the prefixed form (gortex/internal/x.go::Foo). Falls back to the input
// id (which then surfaces the not-found caveat) when neither resolves.
// Safe for any id: a non-symbol id (memory/note/overlay) never matches a
// node, so it is returned unchanged.
func (s *Server) resolveSymbolID(ctx context.Context, id string) string {
	if id == "" || s.graph == nil || s.graph.GetNode(id) != nil {
		return id
	}
	if s.multiIndexer == nil {
		return id
	}
	cwd := SessionCWDFromContext(ctx)
	if cwd == "" {
		return id
	}
	if _, _, prefix, ok := s.multiIndexer.ScopeForCWD(cwd); ok && prefix != "" {
		if cand := prefix + "/" + id; s.graph.GetNode(cand) != nil {
			return cand
		}
	}
	return id
}

// symbolIDArg extracts the required "id" argument and normalizes it via
// resolveSymbolID, so every symbol-id tool accepts repo-relative ids.
func (s *Server) symbolIDArg(ctx context.Context, req mcp.CallToolRequest) (string, error) {
	id, err := req.RequireString("id")
	if err != nil {
		return "", err
	}
	return s.resolveSymbolID(ctx, id), nil
}
