package main

import (
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zzet/gortex/internal/daemon"
)

func restoreSeams() {
	isDaemonRunning = daemon.IsRunning
	spawnDaemon = spawnDetachedDaemon
}

// isolateSpawnLock points the spawn lock + fail marker at a fresh temp
// dir per test so concurrent runs and prior fail-markers don't interfere.
func isolateSpawnLock(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	_ = os.Remove(daemon.SpawnFailMarkerPath())
	t.Cleanup(func() {
		_ = os.Remove(daemon.SpawnFailMarkerPath())
		_ = os.Remove(daemon.SpawnLockPath())
	})
}

func TestEnsureDaemon_AlreadyRunning(t *testing.T) {
	defer restoreSeams()
	var spawned int32
	isDaemonRunning = func() bool { return true }
	spawnDaemon = func() error { atomic.AddInt32(&spawned, 1); return nil }
	if d := ensureDaemonReady(true); d != daemonReady {
		t.Fatalf("want daemonReady, got %d", d)
	}
	if atomic.LoadInt32(&spawned) != 0 {
		t.Fatal("a live daemon must not be re-spawned (and no lock taken)")
	}
}

func TestEnsureDaemon_AutostartOff(t *testing.T) {
	defer restoreSeams()
	isDaemonRunning = func() bool { return false }
	spawnDaemon = func() error { t.Fatal("no spawn when autostart is off"); return nil }
	if d := ensureDaemonReady(false); d != daemonUnavailable {
		t.Fatalf("want daemonUnavailable, got %d", d)
	}
}

func TestEnsureDaemon_SingleFlight(t *testing.T) {
	isolateSpawnLock(t)
	defer restoreSeams()
	var running atomic.Bool
	var spawnCount atomic.Int32
	isDaemonRunning = func() bool { return running.Load() }
	spawnDaemon = func() error {
		spawnCount.Add(1)
		time.Sleep(30 * time.Millisecond) // simulate the spawn window
		running.Store(true)
		return nil
	}
	const K = 8
	var wg sync.WaitGroup
	results := make([]daemonDecision, K)
	for i := 0; i < K; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i] = ensureDaemonReady(true) }(i)
	}
	wg.Wait()
	if got := spawnCount.Load(); got != 1 {
		t.Fatalf("exactly one spawn across %d callers, got %d", K, got)
	}
	for i, r := range results {
		if r == daemonUnavailable {
			t.Fatalf("caller %d should not be unavailable when the spawn succeeded", i)
		}
	}
}

func TestEnsureDaemon_SpawnTimeout(t *testing.T) {
	isolateSpawnLock(t)
	defer restoreSeams()
	isDaemonRunning = func() bool { return false }
	spawnDaemon = func() error { return errors.New("spawn failed") }
	if d := ensureDaemonReady(true); d != daemonUnavailable {
		t.Fatalf("a failed spawn must yield daemonUnavailable, got %d", d)
	}
}

func TestEnsureDaemon_SpawnFailure_SingleAttempt(t *testing.T) {
	isolateSpawnLock(t)
	defer restoreSeams()
	var spawnCount atomic.Int32
	isDaemonRunning = func() bool { return false }
	spawnDaemon = func() error { spawnCount.Add(1); return errors.New("broken spawn") }
	const K = 8
	var wg sync.WaitGroup
	for i := 0; i < K; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = ensureDaemonReady(true) }()
	}
	wg.Wait()
	if got := spawnCount.Load(); got != 1 {
		t.Fatalf("a broken spawn must be attempted exactly once within the cooldown, got %d", got)
	}
}
