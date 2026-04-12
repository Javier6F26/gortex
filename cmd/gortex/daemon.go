package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/daemon"
	"github.com/zzet/gortex/internal/graph"
)

var (
	daemonDetach bool
	daemonTail   int
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Manage the long-living Gortex daemon",
	Long: `The daemon holds the graph for all tracked repositories and serves every
MCP client (Claude Code, Cursor, Kiro, ...) plus the CLI from one shared
index. See spec-daemon.md for the architecture.

If no daemon is running, ` + "`gortex serve`" + ` still works standalone — the daemon
is additive, not required.`,
}

var daemonStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the daemon",
	RunE:  runDaemonStart,
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the daemon gracefully (waits for final snapshot)",
	RunE:  runDaemonStop,
}

var daemonRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Stop and start the daemon (preserves tracked repos)",
	RunE:  runDaemonRestart,
}

var daemonReloadCmd = &cobra.Command{
	Use:   "reload",
	Short: "Re-read config and pick up new or removed repos without restart",
	RunE:  runDaemonReload,
}

var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon PID, uptime, tracked repos, memory, sessions",
	RunE:  runDaemonStatus,
}

var daemonLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Tail the daemon log file",
	RunE:  runDaemonLogs,
}

func init() {
	daemonStartCmd.Flags().BoolVar(&daemonDetach, "detach", false,
		"fork to background after starting (writes to ~/.cache/gortex/daemon.log)")
	daemonLogsCmd.Flags().IntVarP(&daemonTail, "tail", "n", 50,
		"show only the last N log lines")

	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonRestartCmd)
	daemonCmd.AddCommand(daemonReloadCmd)
	daemonCmd.AddCommand(daemonStatusCmd)
	daemonCmd.AddCommand(daemonLogsCmd)
	rootCmd.AddCommand(daemonCmd)
}

// runDaemonStart starts the daemon in foreground (default) or detached
// (when --detach is passed). Detach does a self-exec: re-runs this binary
// with GORTEX_DAEMON_CHILD=1 set, which the inner exec picks up and runs
// the actual serve loop.
func runDaemonStart(cmd *cobra.Command, _ []string) error {
	if daemon.IsRunning() {
		return fmt.Errorf("daemon already running (socket: %s)", daemon.SocketPath())
	}
	if daemonDetach && os.Getenv("GORTEX_DAEMON_CHILD") != "1" {
		return spawnDetachedDaemon()
	}
	logger := newLogger()
	srv := daemon.New(daemon.SocketPath(), version, logger)

	// Build the production state: graph, multi-indexer, config manager,
	// MCP server. The daemon owns one of each — shared by every session
	// that connects.
	state, err := buildDaemonState(logger)
	if err != nil {
		return fmt.Errorf("build daemon state: %w", err)
	}

	srv.Controller = &realController{
		graph:         state.graph,
		multiIndexer:  state.multiIndexer,
		configManager: state.configManager,
		logger:        logger,
		onShutdown: func() error {
			// Order matters: snapshot first (while the indexer may still
			// be in the middle of a write), then flush savings so
			// cumulative totals are consistent with what the next start
			// will load back.
			saveSnapshot(state.graph, version, logger)
			if state.mcpServer != nil {
				_ = state.mcpServer.FlushSavings()
			}
			return nil
		},
	}
	srv.MCPDispatcher = newMCPDispatcher(state.mcpServer, state.multiIndexer, logger)

	// Periodic snapshots — 10 minute interval. On a crash we lose at
	// most one interval's worth of work, which is acceptable given
	// snapshot writes are atomic (tmp → rename) and can never leave a
	// truncated file on disk.
	stopSnapshotter := startPeriodicSnapshots(state.graph, version, 10*time.Minute, logger)
	defer stopSnapshotter()

	if err := srv.Listen(); err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"[gortex daemon] listening on %s (pid %d)\n",
		daemon.SocketPath(), os.Getpid())
	return srv.Serve()
}

// startPeriodicSnapshots kicks off a goroutine that writes a snapshot on
// every tick. Returns a stop function the caller runs at shutdown. The
// final snapshot on shutdown is handled by onShutdown — this loop only
// covers the "crash resilience" case (interval loss vs full re-index).
func startPeriodicSnapshots(g *graph.Graph, version string, interval time.Duration, logger *zap.Logger) func() {
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				saveSnapshot(g, version, logger)
			case <-stop:
				return
			}
		}
	}()
	return func() { close(stop) }
}

// spawnDetachedDaemon re-invokes the binary with GORTEX_DAEMON_CHILD=1
// set, the log redirected to the daemon log file, and the child
// parented to init. Parent exits as soon as the child has the socket up.
func spawnDetachedDaemon() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	if err := daemon.EnsureParentDir(daemon.LogFilePath()); err != nil {
		return err
	}
	logFile, err := os.OpenFile(daemon.LogFilePath(),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	child := exec.Command(exe, "daemon", "start")
	child.Env = append(os.Environ(), "GORTEX_DAEMON_CHILD=1")
	child.Stdout = logFile
	child.Stderr = logFile
	child.Stdin = nil
	// Detach from the controlling terminal so Ctrl-C on the parent
	// doesn't kill the daemon.
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("spawn daemon: %w", err)
	}
	// Don't wait — the child inherits the log file handle.

	// Wait until the socket is live or a timeout hits, so we fail fast
	// if the child died on startup.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if daemon.IsRunning() {
			fmt.Fprintf(os.Stderr, "[gortex daemon] detached (pid %d, log: %s)\n",
				child.Process.Pid, daemon.LogFilePath())
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not come up within 5s; check %s", daemon.LogFilePath())
}

func runDaemonStop(_ *cobra.Command, _ []string) error {
	if !daemon.IsRunning() {
		fmt.Fprintln(os.Stderr, "[gortex daemon] not running")
		return nil
	}
	c, err := daemon.Dial(daemon.Handshake{Mode: daemon.ModeControl, ClientName: "cli"})
	if err != nil {
		// Daemon said it was alive but won't talk — probably a stale PID file
		// the daemon hasn't cleaned up. Fall back to killing by PID.
		return killByPID()
	}
	resp, err := c.Control(daemon.ControlShutdown, nil)
	_ = c.Close()
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("shutdown rejected: %s %s", resp.ErrorCode, resp.ErrorMsg)
	}
	fmt.Fprintln(os.Stderr, "[gortex daemon] stopped")
	return nil
}

func runDaemonRestart(cmd *cobra.Command, args []string) error {
	// Stop is idempotent when not running.
	if err := runDaemonStop(cmd, args); err != nil {
		return err
	}
	// Give the OS a moment to release the socket file.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && daemon.IsRunning() {
		time.Sleep(50 * time.Millisecond)
	}
	daemonDetach = true
	return runDaemonStart(cmd, args)
}

func runDaemonReload(_ *cobra.Command, _ []string) error {
	c, err := daemonControlClient()
	if err != nil {
		return err
	}
	defer c.Close()
	resp, err := c.Control(daemon.ControlReload, nil)
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("reload rejected: %s %s", resp.ErrorCode, resp.ErrorMsg)
	}
	fmt.Fprintln(os.Stderr, "[gortex daemon] reloaded")
	return nil
}

func runDaemonStatus(cmd *cobra.Command, _ []string) error {
	c, err := daemonControlClient()
	if err != nil {
		return err
	}
	defer c.Close()
	resp, err := c.Control(daemon.ControlStatus, nil)
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("status rejected: %s %s", resp.ErrorCode, resp.ErrorMsg)
	}
	var st daemon.StatusResponse
	if err := json.Unmarshal(resp.Result, &st); err != nil {
		return fmt.Errorf("parse status: %w", err)
	}
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "daemon      %s\n", st.Version)
	fmt.Fprintf(w, "pid         %d\n", st.PID)
	fmt.Fprintf(w, "socket      %s\n", st.SocketPath)
	fmt.Fprintf(w, "uptime      %s\n", formatDuration(time.Duration(st.UptimeSeconds)*time.Second))
	fmt.Fprintf(w, "sessions    %d\n", st.Sessions)
	if st.MemoryBytes > 0 {
		fmt.Fprintf(w, "memory      %d bytes\n", st.MemoryBytes)
	}
	if len(st.TrackedRepos) > 0 {
		fmt.Fprintln(w, "tracked repos:")
		for _, r := range st.TrackedRepos {
			fmt.Fprintf(w, "  %-20s %s  (%d files, %d nodes, %d edges)\n",
				r.Prefix, r.Path, r.Files, r.Nodes, r.Edges)
		}
	} else {
		fmt.Fprintln(w, "tracked repos: (none)")
	}
	return nil
}

func runDaemonLogs(cmd *cobra.Command, _ []string) error {
	path := daemon.LogFilePath()
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open log %s: %w", path, err)
	}
	defer f.Close()
	lines, err := tailLines(f, daemonTail)
	if err != nil {
		return err
	}
	for _, l := range lines {
		fmt.Fprintln(cmd.OutOrStdout(), l)
	}
	return nil
}

// daemonControlClient is the shared "dial + expect running" helper for
// the read-only control subcommands. Returns a clear error instead of
// a misleading ErrDaemonUnavailable.
func daemonControlClient() (*daemon.Client, error) {
	c, err := daemon.Dial(daemon.Handshake{Mode: daemon.ModeControl, ClientName: "cli"})
	if err != nil {
		return nil, fmt.Errorf("daemon not reachable (%v) — is it running? Try `gortex daemon start`", err)
	}
	return c, nil
}

// killByPID is the fallback stop path for stale daemons that have a PID
// file but don't respond on the socket. Sends SIGTERM, waits, then
// SIGKILL. Silently returns nil if the PID no longer exists.
func killByPID() error {
	pidBytes, err := os.ReadFile(daemon.PIDFilePath())
	if err != nil {
		return nil
	}
	pid, _ := strconv.Atoi(string(pidBytes))
	if pid <= 0 {
		return nil
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			// Process gone.
			_ = os.Remove(daemon.PIDFilePath())
			_ = os.Remove(daemon.SocketPath())
			fmt.Fprintln(os.Stderr, "[gortex daemon] stopped (via SIGTERM)")
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Last resort.
	_ = syscall.Kill(pid, syscall.SIGKILL)
	_ = os.Remove(daemon.PIDFilePath())
	_ = os.Remove(daemon.SocketPath())
	fmt.Fprintln(os.Stderr, "[gortex daemon] stopped (via SIGKILL)")
	return nil
}

// tailLines returns the last n lines of f. Used by `daemon logs`. Small
// implementation — log files are capped at a few MB so we can afford a
// full read and slice rather than seeking from the end.
func tailLines(f io.Reader, n int) ([]string, error) {
	buf, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	// Split on newline without pulling in bufio.Scanner buffer-size gotchas.
	var out []string
	start := 0
	for i, b := range buf {
		if b == '\n' {
			out = append(out, string(buf[start:i]))
			start = i + 1
		}
	}
	if start < len(buf) {
		out = append(out, string(buf[start:]))
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out, nil
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
}

// stubController is a placeholder Controller so `gortex daemon start`
// works end-to-end before the real MultiIndexer integration lands. It
// accepts track/untrack/reload/status calls, records them, and returns
// empty results. Replaced by the real controller in the next milestone.
type stubController struct {
	tracked []daemon.TrackedRepoStatus
}

func (c *stubController) Track(_ context.Context, p daemon.TrackParams) (json.RawMessage, error) {
	abs, _ := filepath.Abs(p.Path)
	c.tracked = append(c.tracked, daemon.TrackedRepoStatus{
		Prefix: filepath.Base(abs), Path: abs, Name: p.Name, Project: p.Project, Ref: p.Ref,
	})
	return json.RawMessage(fmt.Sprintf(`{"status":"tracked","path":%q}`, abs)), nil
}

func (c *stubController) Untrack(_ context.Context, p daemon.UntrackParams) (json.RawMessage, error) {
	kept := c.tracked[:0]
	var removed int
	for _, r := range c.tracked {
		if r.Prefix == p.PathOrPrefix || r.Path == p.PathOrPrefix {
			removed++
			continue
		}
		kept = append(kept, r)
	}
	c.tracked = kept
	return json.RawMessage(fmt.Sprintf(`{"removed":%d}`, removed)), nil
}

func (c *stubController) Reload(_ context.Context) (json.RawMessage, error) {
	return json.RawMessage(`{"reloaded":true}`), nil
}

func (c *stubController) Status(_ context.Context) (daemon.StatusResponse, error) {
	return daemon.StatusResponse{TrackedRepos: c.tracked}, nil
}

func (c *stubController) Shutdown(_ context.Context) error { return nil }
