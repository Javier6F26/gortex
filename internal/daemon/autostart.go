package daemon

import (
	"os"
	"path/filepath"
	"strings"
)

// ParseAutostart reports whether daemon auto-start is enabled. Default ON.
// Disabled only by an explicit GORTEX_AUTOSTART in {0,false,off,no}
// (case-insensitive). GORTEX_AUTOSTART is the only knob — there is no
// synonym.
func ParseAutostart() bool {
	if v, ok := os.LookupEnv("GORTEX_AUTOSTART"); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "0", "false", "off", "no":
			return false
		}
		return true // any other value (incl. 1/true/on) => on
	}
	return true // unset => default on
}

// SpawnLockPath returns the advisory-lock file guarding daemon auto-start
// so concurrent `gortex mcp` launches on one machine spawn exactly one
// daemon. Co-located with the socket/PID/log under the per-user state dir.
func SpawnLockPath() string {
	if dir, ok := stateDir(); ok {
		return filepath.Join(dir, "daemon.spawn.lock")
	}
	return filepath.Join(os.TempDir(), "gortex-daemon.spawn.lock")
}

// SpawnFailMarkerPath returns the short-lived sentinel a lock holder
// stamps when a spawn fails, so contending callers skip their own spawn
// attempt within the cooldown window instead of each re-paying the spawn
// wait on a broken spawn.
func SpawnFailMarkerPath() string {
	if dir, ok := stateDir(); ok {
		return filepath.Join(dir, "daemon.spawn.fail")
	}
	return filepath.Join(os.TempDir(), "gortex-daemon.spawn.fail")
}
