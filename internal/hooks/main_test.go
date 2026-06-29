package hooks

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain ensures hook tests never write telemetry to the user's real
// ~/.cache/gortex/hook-decisions.jsonl. Tests that want to inspect the
// log redirect it again via t.Setenv to a per-test tmp file.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gortex-hooks-test")
	if err == nil {
		_ = os.Setenv("GORTEX_HOOK_LOG", filepath.Join(dir, "hook-decisions.jsonl"))
		defer func() { _ = os.RemoveAll(dir) }()
	}
	// Default the file-indexed probe to "not indexed" so no test dials a
	// real daemon. Tests needing an indexed verdict stub fileIndexedFn
	// (fakeIndexedBridge / newIndexedBridge / stubBridge) and restore this
	// default on cleanup.
	fileIndexedFn = func(_, _ string) (bool, int) { return false, 0 }
	os.Exit(m.Run())
}
