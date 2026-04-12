package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/daemon"
	"github.com/zzet/gortex/internal/indexer"
	gortexmcp "github.com/zzet/gortex/internal/mcp"
)

// mcpDispatcher routes MCP JSON-RPC frames from daemon sessions to the
// shared *gortexmcp.Server. Every frame returns through
// MCPServer.HandleMessage, which is the public entry point the
// mark3labs/mcp-go library exposes for non-stdio embeddings.
//
// Session isolation is handled by threading the daemon-assigned session
// ID into ctx via gortexmcp.WithSessionID before HandleMessage runs.
// Tool handlers resolve per-client state through Server.sessionFor(ctx).
type mcpDispatcher struct {
	srv          *gortexmcp.Server
	multiIndexer *indexer.MultiIndexer
	logger       *zap.Logger
}

func newMCPDispatcher(srv *gortexmcp.Server, mi *indexer.MultiIndexer, logger *zap.Logger) *mcpDispatcher {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &mcpDispatcher{srv: srv, multiIndexer: mi, logger: logger}
}

// Dispatch implements daemon.MCPDispatcher. It hands the raw JSON-RPC
// frame to MCPServer.HandleMessage and returns the response bytes.
// Empty return value means the client sent a notification (no response).
//
// The session ID from the daemon connection is attached to ctx via
// gortexmcp.WithSessionID so tool handlers reach per-session state
// through sessionFor(ctx) rather than the shared default. This is what
// keeps client A's recent-activity separate from client B's.
func (d *mcpDispatcher) Dispatch(ctx context.Context, sess *daemon.Session, frame []byte) ([]byte, error) {
	if d.srv == nil || d.srv.MCPServer() == nil {
		return nil, fmt.Errorf("mcp dispatcher: no server attached")
	}

	// Fast-path reject untracked cwds. Returns a structured JSON-RPC
	// error the agent can surface in chat ("run `gortex track .`")
	// rather than a silent wrong-result. Skipped when the session has
	// no cwd (the CLI and test harnesses don't set one), so control-
	// flow paths keep working unchanged.
	if sess.CWD != "" && !d.isCWDTracked(sess.CWD) {
		return d.notTrackedError(sess, frame), nil
	}

	ctx = gortexmcp.WithSessionID(ctx, sess.ID)

	// HandleMessage returns either a JSONRPCResponse, a JSONRPCError, or
	// nil (the message was a notification). It never panics on malformed
	// JSON — it returns a JSON-RPC parse-error frame instead.
	reply := d.srv.MCPServer().HandleMessage(ctx, json.RawMessage(frame))
	if reply == nil {
		return nil, nil
	}

	out, err := json.Marshal(reply)
	if err != nil {
		d.logger.Warn("dispatch: marshal reply failed",
			zap.String("session_id", sess.ID), zap.Error(err))
		return nil, fmt.Errorf("marshal reply: %w", err)
	}
	return out, nil
}

// SessionEnded implements daemon.SessionEndedHook. When a proxy
// disconnects, drop its entry from the MCP server's session map so idle
// per-session state doesn't accumulate for the daemon's lifetime.
func (d *mcpDispatcher) SessionEnded(sess *daemon.Session) {
	if d.srv != nil && sess != nil {
		d.srv.ReleaseSession(sess.ID)
	}
}

// isCWDTracked reports whether the proxy's cwd lies inside any tracked
// repo. Equal paths or any subdirectory of a tracked root qualify —
// e.g. a proxy in ~/projects/myapp/internal counts as tracked when
// ~/projects/myapp is in the tracked set.
//
// Returns true when the daemon has no multi-indexer (single-repo mode,
// anything-goes) so we don't accidentally reject valid embedded-style
// sessions during the rollout.
func (d *mcpDispatcher) isCWDTracked(cwd string) bool {
	if d.multiIndexer == nil {
		return true
	}
	cwd = filepath.Clean(cwd)
	for _, meta := range d.multiIndexer.AllMetadata() {
		root := filepath.Clean(meta.RootPath)
		if cwd == root {
			return true
		}
		if strings.HasPrefix(cwd, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// notTrackedError builds a JSON-RPC error frame the agent surfaces to
// the user. The id is echoed from the inbound frame when it's a request;
// a zero id for notifications is fine (clients ignore responses to
// notifications anyway).
//
// Kept structured — error.code uses the MCP-convention -32000 range for
// server-defined errors; error.data carries machine-readable fields
// (error_code, path, suggestion) so a tool UI can offer a one-click
// "track this repo" button without regex-parsing the message string.
func (d *mcpDispatcher) notTrackedError(sess *daemon.Session, inbound []byte) []byte {
	// Pull the request id out of the inbound frame so the response
	// pairs correctly. If parsing fails (malformed frame), send a
	// null id — JSON-RPC clients treat that as "error with no
	// matching request" which is still more informative than
	// silence.
	var peek struct {
		ID json.RawMessage `json:"id"`
	}
	_ = json.Unmarshal(inbound, &peek)

	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      peek.ID,
		"error": map[string]any{
			"code":    -32000,
			"message": fmt.Sprintf("repository not tracked: %s", sess.CWD),
			"data": map[string]any{
				"error_code": "repo_not_tracked",
				"path":       sess.CWD,
				"suggestion": fmt.Sprintf("Run `gortex track %s` to include this repo in the shared graph.", sess.CWD),
			},
		},
	}
	out, _ := json.Marshal(resp)
	return out
}
