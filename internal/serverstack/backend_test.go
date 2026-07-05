package serverstack

import (
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/graph"
)

// TestOpenBackend_MemoryDefault asserts the memory backend (and the empty
// default) returns a usable in-process store.
func TestOpenBackend_MemoryDefault(t *testing.T) {
	for _, name := range []string{"", "memory", "mem", "in-memory"} {
		store, cleanup, err := OpenBackend(name, "", 0, zap.NewNop(), false)
		if err != nil {
			t.Fatalf("OpenBackend(%q): %v", name, err)
		}
		if store == nil {
			t.Fatalf("OpenBackend(%q): nil store", name)
		}
		if _, ok := store.(*graph.Graph); !ok {
			t.Errorf("OpenBackend(%q): want *graph.Graph, got %T", name, store)
		}
		cleanup()
	}
}

// TestOpenBackend_SqliteOpensFile asserts the sqlite backend opens (and
// creates) a store at the resolved path.
func TestOpenBackend_SqliteOpensFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.sqlite")
	store, cleanup, err := OpenBackend("sqlite", path, 0, zap.NewNop(), true)
	if err != nil {
		t.Fatalf("OpenBackend(sqlite): %v", err)
	}
	if store == nil {
		t.Fatal("nil sqlite store")
	}
	cleanup()
}

// TestOpenBackend_Unknown asserts only memory|sqlite|postgres are accepted
// — a stale backend name (e.g. the removed ladybug) errors rather than
// silently falling back.
func TestOpenBackend_Unknown(t *testing.T) {
	if _, _, err := OpenBackend("ladybug", "", 0, zap.NewNop(), false); err == nil {
		t.Fatal("an unknown backend must error")
	}
}

// TestOpenBackend_PostgresNoDSN asserts the postgres backend requires a DSN.
func TestOpenBackend_PostgresNoDSN(t *testing.T) {
	_, _, err := OpenBackend("postgres", "", 0, zap.NewNop(), false)
	if err == nil {
		t.Fatal("expected error for postgres backend without DSN")
	}
	// Error message should mention DSN.
	if !strings.Contains(err.Error(), "DSN") && !strings.Contains(err.Error(), "dsn") {
		t.Errorf("expected DSN-related error, got: %v", err)
	}
}

// TestOpenBackend_PostgresDSNPropagation asserts the DSN is propagated
// to the store config. Uses a known-bad address; the error should mention
// the connection attempt, not a config-parse issue.
func TestOpenBackend_PostgresDSNPropagation(t *testing.T) {
	_, _, err := OpenBackend("postgres", "postgres://localhost:1/gortex_test?sslmode=disable&connect_timeout=3", 0, zap.NewNop(), false)
	// This should fail because there's nothing on :1, but not with a
	// "DSN required" error — the DSN was accepted, the connection failed.
	if err == nil {
		t.Skip("no pg at localhost:1 — expected failure, skipping")
	}
	// The error should come from the connection layer, not from config parsing.
	if strings.Contains(err.Error(), "DSN required") || strings.Contains(err.Error(), "required") {
		t.Fatalf("DSN was parsed but connection should fail; got: %v", err)
	}
}

// TestOpenBackend_PostgresViaPGShortcut asserts the "pg" alias works.
func TestOpenBackend_PostgresViaPGShortcut(t *testing.T) {
	_, _, err := OpenBackend("pg", "", 0, zap.NewNop(), false)
	if err == nil {
		t.Fatal("expected error for pg backend without DSN")
	}
}
