package savings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestOpen_MissingFile_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "savings.json")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	snap := s.Snapshot()
	if snap.Totals.CallsCounted != 0 {
		t.Errorf("new store has CallsCounted=%d, want 0", snap.Totals.CallsCounted)
	}
	if snap.Version != schemaVersion {
		t.Errorf("new store version=%d, want %d", snap.Version, schemaVersion)
	}
}

func TestAddObservation_AccumulatesAndPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "savings.json")

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for range flushEvery + 5 {
		s.AddObservation("/some/repo", 100, 900)
	}
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Re-open and verify totals survived the write.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	snap := s2.Snapshot()
	if got, want := snap.Totals.CallsCounted, int64(flushEvery+5); got != want {
		t.Errorf("CallsCounted = %d, want %d", got, want)
	}
	if got, want := snap.Totals.TokensSaved, int64((flushEvery+5)*900); got != want {
		t.Errorf("TokensSaved = %d, want %d", got, want)
	}
	if got, want := snap.Totals.TokensReturned, int64((flushEvery+5)*100); got != want {
		t.Errorf("TokensReturned = %d, want %d", got, want)
	}
	if len(snap.PerRepo) != 1 {
		t.Errorf("PerRepo size = %d, want 1", len(snap.PerRepo))
	}
}

func TestAddObservation_ConcurrentSafe(t *testing.T) {
	s, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	const workers = 8
	const per = 250
	var wg sync.WaitGroup
	var expectedSaved atomic.Int64
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range per {
				s.AddObservation("", 10, 100)
				expectedSaved.Add(100)
			}
		}()
	}
	wg.Wait()

	snap := s.Snapshot()
	if got, want := snap.Totals.CallsCounted, int64(workers*per); got != want {
		t.Errorf("CallsCounted = %d, want %d", got, want)
	}
	if got, want := snap.Totals.TokensSaved, expectedSaved.Load(); got != want {
		t.Errorf("TokensSaved = %d, want %d", got, want)
	}
}

func TestOpen_CorruptFile_IsBackedUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "savings.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open should recover from corrupt file, got error: %v", err)
	}
	snap := s.Snapshot()
	if snap.Totals.CallsCounted != 0 {
		t.Errorf("corrupt recovery should start fresh, got CallsCounted=%d", snap.Totals.CallsCounted)
	}
	// Backup should exist.
	matches, _ := filepath.Glob(path + ".corrupt-*")
	if len(matches) == 0 {
		t.Errorf("expected a .corrupt-* backup file in %s", dir)
	}
}

func TestReset_ClearsStateAndRemovesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "savings.json")

	s, _ := Open(path)
	s.AddObservation("/r", 50, 500)
	_ = s.Flush()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file to exist after flush: %v", err)
	}
	if err := s.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected savings.json removed after reset, got %v", err)
	}
	snap := s.Snapshot()
	if snap.Totals.CallsCounted != 0 {
		t.Errorf("in-memory state should be cleared after reset, got CallsCounted=%d", snap.Totals.CallsCounted)
	}
}

func TestOpen_EmptyPath_InMemoryOnly(t *testing.T) {
	s, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	s.AddObservation("r", 10, 100)
	if err := s.Flush(); err != nil {
		t.Errorf("Flush on in-memory store should no-op, got: %v", err)
	}
	snap := s.Snapshot()
	if snap.Totals.CallsCounted != 1 {
		t.Errorf("in-memory store should track, got CallsCounted=%d", snap.Totals.CallsCounted)
	}
}

func TestFile_Schema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "savings.json")

	s, _ := Open(path)
	s.AddObservation("/repo-a", 10, 100)
	_ = s.Flush()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("written file is not JSON: %v\n%s", err, data)
	}
	for _, key := range []string{"version", "first_seen", "last_updated", "totals", "per_repo"} {
		if _, ok := parsed[key]; !ok {
			t.Errorf("missing top-level key %q in persisted file", key)
		}
	}
}
