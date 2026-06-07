package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzet/gortex/internal/daemon"
)

// TestServerAdd_ReadOnlyPersisted asserts `daemon server add --read-only`
// writes read_only = true into the roster entry and that it reloads as a
// read-only remote.
func TestServerAdd_ReadOnlyPersisted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "servers.toml")
	t.Setenv("GORTEX_DAEMON_SERVERS", path)

	// Drive the command's package-level flag vars directly.
	daemonServerAddURL = "https://r2.example:4747"
	daemonServerAddReadOnly = true
	daemonServerAddDefault = false
	daemonServerAddAuthToken = ""
	daemonServerAddAuthTokenEnv = ""
	daemonServerAddWorkspaces = nil
	t.Cleanup(func() {
		daemonServerAddURL = ""
		daemonServerAddReadOnly = false
	})

	if err := runDaemonServerAdd(nil, []string{"r2"}); err != nil {
		t.Fatalf("runDaemonServerAdd: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "read_only = true") {
		t.Fatalf("roster should carry read_only = true, got:\n%s", data)
	}

	cfg, err := daemon.LoadServersConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Server) != 1 || !cfg.Server[0].ReadOnly {
		t.Fatalf("reloaded entry should be read-only, got %+v", cfg.Server)
	}
}
