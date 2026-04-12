package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// echoDispatcher returns a canned JSON-RPC response whenever a frame
// comes in. Used to prove that (a) the daemon delivers frames to the
// dispatcher with the right session context, and (b) the dispatcher's
// reply round-trips back to the client over the socket. Swapping in a
// real *mcp.Server-backed dispatcher is the job of package main, not
// the daemon package itself — that's where the tool registry lives.
type echoDispatcher struct {
	seen chan echoFrame
}

type echoFrame struct {
	sessionID string
	cwd       string
	frame     string
}

func (e *echoDispatcher) Dispatch(_ context.Context, sess *Session, frame []byte) ([]byte, error) {
	e.seen <- echoFrame{sessionID: sess.ID, cwd: sess.CWD, frame: string(frame)}
	// Synthesize a well-formed JSON-RPC response so the proxy side can
	// parse it without extra helpers.
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"result":  map[string]any{"session_id": sess.ID, "echoed": string(frame)},
	}
	return json.Marshal(resp)
}

// TestDaemon_MCPRoundTrip verifies the full happy path: proxy dials in
// MCP mode, sends a frame, daemon delivers it to the dispatcher with
// session context, response flows back to the proxy.
func TestDaemon_MCPRoundTrip(t *testing.T) {
	disp := &echoDispatcher{seen: make(chan echoFrame, 4)}
	dir, err := os.MkdirTemp("/tmp", "gx")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(dir) }()
	socket := filepath.Join(dir, "s")
	t.Setenv("GORTEX_DAEMON_SOCKET", socket)
	t.Setenv("GORTEX_DAEMON_PIDFILE", filepath.Join(dir, "p"))

	srv := New(socket, "test", zap.NewNop())
	srv.MCPDispatcher = disp
	srv.Controller = &fakeController{}
	require.NoError(t, srv.Listen())
	go func() { _ = srv.Serve() }()
	defer func() { _ = srv.Shutdown() }()

	require.Eventually(t, func() bool { return IsRunningAt(socket) },
		2*time.Second, 10*time.Millisecond)

	client, err := DialTo(socket, Handshake{
		Mode:       ModeMCP,
		CWD:        "/tmp/fake-repo",
		ClientName: "claude-code",
	})
	require.NoError(t, err)
	defer client.Close()

	require.NotEmpty(t, client.Ack.SessionID)

	// Send a frame; proxy expects to read the paired response.
	rpc := `{"jsonrpc":"2.0","id":1,"method":"graph_stats","params":{}}`
	require.NoError(t, client.WriteMCPFrame([]byte(rpc)))

	// Dispatcher saw the frame with the right session context.
	select {
	case f := <-disp.seen:
		assert.Equal(t, client.Ack.SessionID, f.sessionID)
		assert.Equal(t, "/tmp/fake-repo", f.cwd)
		assert.Contains(t, f.frame, `"method":"graph_stats"`)
	case <-time.After(2 * time.Second):
		t.Fatal("dispatcher never received the frame")
	}

	// Response made it back through the socket. Read one line.
	replyBytes, err := client.ReadMCPFrame()
	require.NoError(t, err)
	var reply map[string]any
	require.NoError(t, json.Unmarshal(replyBytes, &reply))
	result, ok := reply["result"].(map[string]any)
	require.True(t, ok, "result must be an object: %v", reply)
	assert.Equal(t, client.Ack.SessionID, result["session_id"])
}

// TestDaemon_MCPNoDispatcher verifies the daemon rejects MCP traffic
// cleanly when no dispatcher is attached — caller sees a JSON-RPC
// error frame rather than a hang or a broken connection.
func TestDaemon_MCPNoDispatcher(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "gx")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(dir) }()
	socket := filepath.Join(dir, "s")
	t.Setenv("GORTEX_DAEMON_SOCKET", socket)
	t.Setenv("GORTEX_DAEMON_PIDFILE", filepath.Join(dir, "p"))

	srv := New(socket, "test", zap.NewNop())
	srv.Controller = &fakeController{}
	// Intentionally: no MCPDispatcher.
	require.NoError(t, srv.Listen())
	go func() { _ = srv.Serve() }()
	defer func() { _ = srv.Shutdown() }()

	require.Eventually(t, func() bool { return IsRunningAt(socket) },
		2*time.Second, 10*time.Millisecond)

	client, err := DialTo(socket, Handshake{Mode: ModeMCP, CWD: "/tmp/x"})
	require.NoError(t, err)
	defer client.Close()

	replyBytes, err := client.ReadMCPFrame()
	require.NoError(t, err)
	var reply map[string]any
	require.NoError(t, json.Unmarshal(replyBytes, &reply))
	errObj, ok := reply["error"].(map[string]any)
	require.True(t, ok, "expected JSON-RPC error, got: %v", reply)
	msg, _ := errObj["message"].(string)
	assert.Contains(t, msg, "control-only")
}

// assertNoPanic keeps imports tidy for the fmt package when tests grow.
var _ = fmt.Sprintf

// hookDispatcher satisfies both MCPDispatcher and SessionEndedHook so we
// can prove the daemon fires the disconnect callback when a proxy closes.
type hookDispatcher struct {
	ended chan string // receives the session ID that ended
}

func (h *hookDispatcher) Dispatch(_ context.Context, _ *Session, _ []byte) ([]byte, error) {
	return nil, nil
}

func (h *hookDispatcher) SessionEnded(sess *Session) {
	h.ended <- sess.ID
}

// TestDaemon_SessionEndedHook_FiresOnDisconnect pins the contract that
// dispatchers implementing SessionEndedHook get notified when a proxy
// closes its connection. Without this, per-session state allocated in
// the dispatcher would leak for the daemon's lifetime.
func TestDaemon_SessionEndedHook_FiresOnDisconnect(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "gx")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(dir) }()
	socket := filepath.Join(dir, "s")
	t.Setenv("GORTEX_DAEMON_SOCKET", socket)
	t.Setenv("GORTEX_DAEMON_PIDFILE", filepath.Join(dir, "p"))

	hook := &hookDispatcher{ended: make(chan string, 2)}
	srv := New(socket, "test", zap.NewNop())
	srv.MCPDispatcher = hook
	srv.Controller = &fakeController{}
	require.NoError(t, srv.Listen())
	go func() { _ = srv.Serve() }()
	defer func() { _ = srv.Shutdown() }()

	require.Eventually(t, func() bool { return IsRunningAt(socket) },
		2*time.Second, 10*time.Millisecond)

	client, err := DialTo(socket, Handshake{Mode: ModeMCP, CWD: "/tmp/x"})
	require.NoError(t, err)
	sessionID := client.Ack.SessionID
	require.NoError(t, client.Close())

	select {
	case got := <-hook.ended:
		assert.Equal(t, sessionID, got,
			"SessionEnded must receive the ID of the disconnected session")
	case <-time.After(2 * time.Second):
		t.Fatal("SessionEnded hook never fired after client close")
	}
}
