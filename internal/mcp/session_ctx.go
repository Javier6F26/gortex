package mcp

import (
	"context"
	"sync"
)

// sessionCtxKey is the private context key under which a caller
// (typically the daemon's MCP dispatcher) stashes the session ID for
// the current request. The value is read by `Server.sessionFor` so
// tool handlers resolve to the correct per-client state.
//
// Unexported so external packages can't inject one accidentally — use
// WithSessionID / SessionIDFromContext.
type sessionCtxKey struct{}

// WithSessionID returns a context carrying id. The daemon's MCP
// dispatcher wraps each inbound frame's context with this before
// calling MCPServer.HandleMessage, giving every tool handler access
// to the per-session state without touching the handler signature.
//
// An empty id is treated as "no session" and returns ctx unchanged —
// that's the path the embedded stdio server takes, where there's only
// one implicit session.
func WithSessionID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionCtxKey{}, id)
}

// SessionIDFromContext returns the session ID attached via
// WithSessionID, or "" when none is present. Callers treat "" as
// "default shared session" — the same state the embedded server uses.
func SessionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(sessionCtxKey{}).(string); ok {
		return id
	}
	return ""
}

// sessionLocal bundles the per-client state that should not aggregate
// across sessions: recent agent activity (viewed/modified files and
// symbols), and session-scoped counters. Shared pieces — the graph,
// feedback store, cumulative savings — stay on *Server directly.
type sessionLocal struct {
	session *sessionState
}

// newSessionLocal constructs a fresh per-session state container.
func newSessionLocal() *sessionLocal {
	return &sessionLocal{session: newSessionState()}
}

// sessionMap is a thread-safe string→*sessionLocal registry. Used by
// *Server to multiplex session-scoped state when running inside the
// daemon. The embedded / stdio server path doesn't consult this map;
// it reads *Server.session directly.
type sessionMap struct {
	mu       sync.Mutex
	sessions map[string]*sessionLocal
}

func newSessionMap() *sessionMap {
	return &sessionMap{sessions: make(map[string]*sessionLocal)}
}

// get returns the session state for id, creating it if absent. Never
// returns nil — a missing entry is created lazily. Thread-safe.
func (m *sessionMap) get(id string) *sessionLocal {
	m.mu.Lock()
	defer m.mu.Unlock()
	sl, ok := m.sessions[id]
	if !ok {
		sl = newSessionLocal()
		m.sessions[id] = sl
	}
	return sl
}

// release drops the session entry for id. Called when the daemon's
// accept loop sees a proxy disconnect.
func (m *sessionMap) release(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, id)
}
