package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzet/gortex/internal/daemon"
)

// TestServerAddRemove_NoStaleRestartNotice asserts roster add/remove route
// through the live-reload path (proxyApplyToRunningDaemon) and no longer
// print the misleading "run gortex daemon restart" notice. The socket is
// pointed at a dead path so IsRunning() is false and no real daemon is
// dialed — the add/remove must still succeed and stay silent about restarts.
func TestServerAddRemove_NoStaleRestartNotice(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GORTEX_DAEMON_SERVERS", filepath.Join(dir, "servers.toml"))
	t.Setenv("GORTEX_DAEMON_SOCKET", filepath.Join(dir, "dead.sock"))

	daemonServerAddURL = "https://r2.example:4747"
	t.Cleanup(func() { daemonServerAddURL = "" })

	capture := func(fn func() error) (string, error) {
		old := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w
		err := fn()
		_ = w.Close()
		os.Stderr = old
		out, _ := io.ReadAll(r)
		return string(out), err
	}

	addOut, err := capture(func() error { return runDaemonServerAdd(nil, []string{"r2"}) })
	if err != nil {
		t.Fatalf("runDaemonServerAdd: %v", err)
	}
	if strings.Contains(addOut, "daemon restart") {
		t.Errorf("add must not print the stale restart notice; got:\n%s", addOut)
	}

	// The entry actually persisted.
	cfg, err := daemon.LoadServersConfig(filepath.Join(dir, "servers.toml"))
	if err != nil || cfg.FindBySlug("r2") == nil {
		t.Fatalf("server r2 should have persisted; cfg=%+v err=%v", cfg, err)
	}

	rmOut, err := capture(func() error { return runDaemonServerRemove(nil, []string{"r2"}) })
	if err != nil {
		t.Fatalf("runDaemonServerRemove: %v", err)
	}
	if strings.Contains(rmOut, "daemon restart") {
		t.Errorf("remove must not print the stale restart notice; got:\n%s", rmOut)
	}
}
