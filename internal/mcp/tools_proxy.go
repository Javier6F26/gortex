package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
)

// RemoteOverrideSink lets the per-session proxy-toggle tools push an
// enable/disable override for a remote into the daemon's session state,
// and read the roster's effective state. It is implemented by the daemon
// (which owns the session registry + the live router) and wired via
// SetRemoteOverrideSink; nil in embedded mode (no per-connection daemon
// session), where the tools return a "use the global CLI" message.
type RemoteOverrideSink interface {
	// SetRemoteOverride records a session-scoped enable/disable for a
	// remote slug. Errors on an unknown slug or a missing session.
	SetRemoteOverride(sessionID, slug string, enabled bool) error
	// ClearRemoteOverride drops a session override so the remote reverts
	// to its global enabled state.
	ClearRemoteOverride(sessionID, slug string) error
	// RemoteRosterStatus returns each roster remote's global + effective
	// enabled state for the given session.
	RemoteRosterStatus(sessionID string) ([]RemoteRosterStatus, error)
}

// RemoteRosterStatus is one roster remote's enabled-state view for a
// session: the persistent global state, the session override (nil when
// none), and the resulting effective state.
type RemoteRosterStatus struct {
	Slug            string `json:"slug"`
	GlobalEnabled   bool   `json:"global_enabled"`
	SessionOverride *bool  `json:"session_override,omitempty"`
	Effective       bool   `json:"effective"`
}

// SetRemoteOverrideSink wires the daemon-backed remote-override bridge
// and registers the session proxy-toggle tools once. Idempotent.
func (s *Server) SetRemoteOverrideSink(sink RemoteOverrideSink) {
	s.remoteOverrides = sink
	if sink == nil {
		return
	}
	s.registerProxyToolsOnce.Do(func() { s.registerProxyTools() })
}

func (s *Server) registerProxyTools() {
	s.mcpServer.AddTool(
		mcp.NewTool("proxy_enable",
			mcp.WithDescription("Enable federation/proxy to a remote Gortex daemon FOR THIS MCP SESSION ONLY. Overrides the global enabled state for the session's lifetime and auto-reverts on disconnect. For a persistent global change use `gortex proxy on <slug>`."),
			mcp.WithString("slug", mcp.Required(), mcp.Description("Roster slug of the remote to enable for this session.")),
		),
		s.handleProxyEnable,
	)
	s.mcpServer.AddTool(
		mcp.NewTool("proxy_disable",
			mcp.WithDescription("Disable federation/proxy to a remote Gortex daemon FOR THIS MCP SESSION ONLY. Beats the global enabled state for the session's lifetime; queries from this session skip the remote. Auto-reverts on disconnect. For a persistent global change use `gortex proxy off <slug>`."),
			mcp.WithString("slug", mcp.Required(), mcp.Description("Roster slug of the remote to disable for this session.")),
		),
		s.handleProxyDisable,
	)
	s.mcpServer.AddTool(
		mcp.NewTool("proxy_status",
			mcp.WithDescription("List every roster remote with its global enabled state, this session's override (if any), and the resulting effective enabled state."),
		),
		s.handleProxyStatus,
	)
}

func (s *Server) handleProxyEnable(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return s.handleProxyToggle(ctx, req, true)
}

func (s *Server) handleProxyDisable(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return s.handleProxyToggle(ctx, req, false)
}

func (s *Server) handleProxyToggle(ctx context.Context, req mcp.CallToolRequest, enabled bool) (*mcp.CallToolResult, error) {
	slug, err := req.RequireString("slug")
	if err != nil {
		return mcp.NewToolResultError("slug is required"), nil
	}
	if s.remoteOverrides == nil {
		return s.sessionToggleUnavailable(), nil
	}
	sid := SessionIDFromContext(ctx)
	if sid == "" {
		return s.sessionToggleUnavailable(), nil
	}
	if err := s.remoteOverrides.SetRemoteOverride(sid, slug, enabled); err != nil {
		return NewStructuredErrorResult(StructuredError{
			ErrorCode: ErrCodeInvalidArgument,
			Message:   err.Error(),
			Data:      map[string]any{"slug": slug},
		}), nil
	}
	state := "disabled"
	if enabled {
		state = "enabled"
	}
	return s.respondJSONOrTOON(ctx, req, map[string]any{
		"slug":          slug,
		"session_state": state,
		"note":          "session-scoped — auto-reverts on disconnect. Use `gortex proxy on/off` for a persistent global change.",
	})
}

func (s *Server) handleProxyStatus(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if s.remoteOverrides == nil {
		return s.respondJSONOrTOON(ctx, req, map[string]any{
			"remotes": []any{},
			"note":    "no daemon-backed session; inspect remotes with `gortex proxy status`.",
		})
	}
	rows, err := s.remoteOverrides.RemoteRosterStatus(SessionIDFromContext(ctx))
	if err != nil {
		return NewStructuredErrorResult(StructuredError{
			ErrorCode: ErrCodeInvalidArgument,
			Message:   err.Error(),
		}), nil
	}
	if rows == nil {
		rows = []RemoteRosterStatus{}
	}
	return s.respondJSONOrTOON(ctx, req, map[string]any{"remotes": rows})
}

func (s *Server) sessionToggleUnavailable() *mcp.CallToolResult {
	return NewStructuredErrorResult(StructuredError{
		ErrorCode: ErrCodeToolBlockedByMode,
		Message: "session proxy toggle requires a daemon-backed MCP session; use " +
			"`gortex proxy on/off <slug>` for global control",
	})
}
