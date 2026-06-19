package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
)

// Client is one end of a daemon connection. Used by:
//   - `gortex mcp --proxy` — stdio MCP → daemon relay.
//   - `gortex daemon status / reload / track / untrack / stop` — control RPC.
//
// A Client is not safe for concurrent use — each caller owns its own.
type Client struct {
	Conn   net.Conn
	reader *bufio.Reader
	Ack    HandshakeAck
}

// Dial connects to the daemon on the default socket path and completes a
// handshake. Returns (nil, err) if the socket doesn't exist, the daemon
// isn't reachable, or the handshake is rejected.
//
// Callers in a fallback path (proxy mode, CLI commands that can work
// standalone) should treat ErrDaemonUnavailable distinctly from other
// errors and continue as if the daemon isn't there.
func Dial(h Handshake) (*Client, error) {
	return DialTo(SocketPath(), h)
}

// DialTo is Dial with an explicit socket path. Useful for tests and for
// clients that want to talk to a daemon on a non-default socket.
func DialTo(socketPath string, h Handshake) (*Client, error) {
	d := &net.Dialer{Timeout: 500 * time.Millisecond}
	conn, err := d.Dial("unix", socketPath)
	if err != nil {
		if isNoDaemonErr(err) {
			return nil, fmt.Errorf("%w: %v", ErrDaemonUnavailable, err)
		}
		return nil, err
	}

	if h.Version == 0 {
		h.Version = ProtocolVersion
	}
	if h.PID == 0 {
		h.PID = os.Getpid()
	}
	if err := WriteJSONLine(conn, h); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("send handshake: %w", err)
	}
	reader := bufio.NewReader(conn)
	ackLine, err := reader.ReadBytes('\n')
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read handshake ack: %w", err)
	}
	var ack HandshakeAck
	if err := json.Unmarshal(ackLine, &ack); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("parse handshake ack: %w", err)
	}
	if !ack.OK {
		_ = conn.Close()
		// A protocol-version rejection is recoverable: wrap the sentinel so the
		// proxy can fall back to the embedded server instead of failing.
		if ack.ErrorCode == ErrProtocolMismatch {
			return nil, fmt.Errorf("%w: %s", ErrProtocolVersionMismatch, ack.ErrorMsg)
		}
		return nil, fmt.Errorf("daemon rejected handshake [%s]: %s", ack.ErrorCode, ack.ErrorMsg)
	}
	return &Client{Conn: conn, reader: reader, Ack: ack}, nil
}

// Close tears down the connection.
func (c *Client) Close() error {
	if c == nil || c.Conn == nil {
		return nil
	}
	return c.Conn.Close()
}

// Control sends one control request and returns the paired response.
// Fails if the Client was opened in ModeMCP.
func (c *Client) Control(kind string, params any) (ControlResponse, error) {
	var raw json.RawMessage
	if params != nil {
		var err error
		raw, err = json.Marshal(params)
		if err != nil {
			return ControlResponse{}, fmt.Errorf("marshal params: %w", err)
		}
	}
	req := ControlRequest{Kind: kind, Params: raw}
	if err := WriteJSONLine(c.Conn, req); err != nil {
		return ControlResponse{}, fmt.Errorf("send control request: %w", err)
	}
	line, err := c.reader.ReadBytes('\n')
	if err != nil {
		return ControlResponse{}, fmt.Errorf("read control response: %w", err)
	}
	var resp ControlResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return ControlResponse{}, fmt.Errorf("parse control response: %w", err)
	}
	return resp, nil
}

// WriteMCPFrame writes a raw MCP JSON-RPC frame to the daemon. Caller is
// responsible for ensuring the frame is valid JSON-RPC; the daemon
// passes it through to the session's MCPDispatcher.
func (c *Client) WriteMCPFrame(frame []byte) error {
	if _, err := c.Conn.Write(frame); err != nil {
		return err
	}
	// Ensure newline-delimited framing.
	if n := len(frame); n == 0 || frame[n-1] != '\n' {
		if _, err := c.Conn.Write([]byte{'\n'}); err != nil {
			return err
		}
	}
	return nil
}

// ReadMCPFrame reads one MCP JSON-RPC frame from the daemon. Returns
// io.EOF when the daemon closes the connection.
func (c *Client) ReadMCPFrame() ([]byte, error) {
	line, err := c.reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	return line, nil
}

// ErrDaemonUnavailable is the sentinel Dial returns when the daemon
// isn't reachable. Callers in fallback paths should errors.Is() against
// this to distinguish "daemon just isn't running" from real errors
// (permissions, handshake rejection, etc.) where silent fallback would
// mask a bug.
var ErrDaemonUnavailable = errors.New("daemon unavailable")

// ErrProtocolVersionMismatch is returned by Dial when the daemon answers the
// handshake with a protocol_mismatch rejection — the running daemon speaks a
// different wire version than this binary (a stale daemon after an upgrade).
// Like ErrDaemonUnavailable it is recoverable: the caller should fall back to
// the embedded in-process server rather than fail, so an in-flight editor
// session keeps working across a version skew.
var ErrProtocolVersionMismatch = errors.New("daemon protocol version mismatch")

// ShouldFallBackToEmbedded reports whether a Dial error is one the MCP proxy
// can recover from by running the embedded in-process server instead: the
// daemon isn't running, or it is running a mismatched protocol version. Any
// other error (permissions, a genuinely broken socket) is a real failure the
// caller must surface.
func ShouldFallBackToEmbedded(err error) bool {
	return errors.Is(err, ErrDaemonUnavailable) || errors.Is(err, ErrProtocolVersionMismatch)
}

// isNoDaemonErr returns true for the subset of dial errors that mean
// "the daemon probably isn't running" as opposed to "your system is
// broken." We treat connection-refused, no-such-file, and timeout as
// "unavailable" because spinning up the daemon or falling back is the
// right answer to all three.
func isNoDaemonErr(err error) bool {
	var se syscall.Errno
	if errors.As(err, &se) {
		switch se {
		case syscall.ECONNREFUSED, syscall.ENOENT:
			return true
		}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	// net.OpError wrapping "no such file or directory" is how Go reports
	// a missing socket path on some platforms.
	var op *net.OpError
	if errors.As(err, &op) {
		if errors.Is(op.Err, syscall.ECONNREFUSED) || errors.Is(op.Err, syscall.ENOENT) {
			return true
		}
	}
	return false
}

// IsRunning returns true when a daemon is reachable on the default socket.
// A thin convenience for CLI paths that want to branch on availability
// without constructing a full handshake.
func IsRunning() bool {
	return IsRunningAt(SocketPath())
}

// IsRunningAt is IsRunning with an explicit socket path.
func IsRunningAt(socketPath string) bool {
	d := &net.Dialer{Timeout: 200 * time.Millisecond}
	conn, err := d.Dial("unix", socketPath)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
