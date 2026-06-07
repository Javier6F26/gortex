package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseAutostart(t *testing.T) {
	cases := map[string]bool{
		"0": false, "false": false, "off": false, "no": false, "OFF": false, " no ": false,
		"1": true, "true": true, "on": true, "anything": true, "": true,
	}
	for v, want := range cases {
		t.Setenv("GORTEX_AUTOSTART", v)
		if got := ParseAutostart(); got != want {
			t.Errorf("GORTEX_AUTOSTART=%q => %v, want %v", v, got, want)
		}
	}
}

func TestParseAutostart_UnsetDefaultsOn(t *testing.T) {
	if v, ok := os.LookupEnv("GORTEX_AUTOSTART"); ok {
		_ = os.Unsetenv("GORTEX_AUTOSTART")
		t.Cleanup(func() { _ = os.Setenv("GORTEX_AUTOSTART", v) })
	}
	if !ParseAutostart() {
		t.Error("unset GORTEX_AUTOSTART must default on")
	}
}

func TestParseAutostart_NoSynonym(t *testing.T) {
	// GORTEX_NO_AUTOSTART is not consulted — only GORTEX_AUTOSTART is.
	t.Setenv("GORTEX_NO_AUTOSTART", "1")
	if v, ok := os.LookupEnv("GORTEX_AUTOSTART"); ok {
		_ = os.Unsetenv("GORTEX_AUTOSTART")
		t.Cleanup(func() { _ = os.Setenv("GORTEX_AUTOSTART", v) })
	}
	if !ParseAutostart() {
		t.Error("GORTEX_NO_AUTOSTART must not disable autostart")
	}
}

func TestSpawnLockPath_UnderStateDir(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	lock := SpawnLockPath()
	fail := SpawnFailMarkerPath()
	if filepath.Base(lock) != "daemon.spawn.lock" {
		t.Errorf("lock basename = %q", filepath.Base(lock))
	}
	if filepath.Base(fail) != "daemon.spawn.fail" {
		t.Errorf("fail-marker basename = %q", filepath.Base(fail))
	}
	if filepath.Dir(lock) != filepath.Dir(fail) {
		t.Error("lock and fail-marker should be siblings")
	}
}
