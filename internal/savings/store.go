// Package savings persists cumulative token-savings metrics across server
// restarts. Every source-reading tool call feeds this store through the MCP
// server's tokenStats, so over time the numbers become a credible narrative:
// "Gortex saved N tokens / $X at model rate this month".
//
// Storage format: a single JSON file at ~/.cache/gortex/savings.json (or the
// configured cache dir). Atomic writes via temp-file + rename. Falls back to
// an in-memory-only store when the path isn't writable.
package savings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// schemaVersion lets future changes migrate or reject incompatible files.
	schemaVersion = 1
	// flushEvery buffers this many observations before writing to disk.
	flushEvery = 20
)

// Totals is the cumulative record for a single scope (top-level or per-repo).
type Totals struct {
	TokensSaved    int64 `json:"tokens_saved"`
	TokensReturned int64 `json:"tokens_returned"`
	CallsCounted   int64 `json:"calls_counted"`
}

// File is the on-disk schema.
type File struct {
	Version       int                `json:"version"`
	FirstSeen     time.Time          `json:"first_seen"`
	LastUpdated   time.Time          `json:"last_updated"`
	Totals        Totals             `json:"totals"`
	PerRepo       map[string]*Totals `json:"per_repo,omitempty"`
}

// Store holds the cumulative savings state and flushes to disk periodically.
// All operations are safe for concurrent use. When path is empty the store
// still tracks in-memory but never writes to disk.
type Store struct {
	mu      sync.Mutex
	path    string
	file    File
	pending int // observations since last flush
}

// DefaultPath returns the canonical savings.json location under the user's
// cache dir. Returns an empty string (i.e. "disable persistence") when the
// cache dir is unavailable.
func DefaultPath() string {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		return ""
	}
	return filepath.Join(base, "gortex", "savings.json")
}

// Open loads savings from path, or returns an empty Store when the file
// doesn't exist yet. Corrupt or incompatible files are backed up to
// `<path>.corrupt-<ts>` and replaced with a fresh state so a bad write can't
// permanently break metrics.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	s.file.Version = schemaVersion
	s.file.FirstSeen = time.Now().UTC()
	s.file.PerRepo = make(map[string]*Totals)

	if path == "" {
		return s, nil
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("read savings: %w", err)
	}

	var loaded File
	if jerr := json.Unmarshal(data, &loaded); jerr != nil || loaded.Version != schemaVersion {
		backup := fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix())
		_ = os.Rename(path, backup)
		return s, nil
	}
	if loaded.PerRepo == nil {
		loaded.PerRepo = make(map[string]*Totals)
	}
	s.file = loaded
	return s, nil
}

// AddObservation increments the store by one source-reading tool call. When
// repoPath is non-empty, the totals are also aggregated under that key for
// per-project reporting. Writes to disk every flushEvery observations.
func (s *Store) AddObservation(repoPath string, returned, saved int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if saved < 0 {
		saved = 0
	}

	s.file.Totals.TokensSaved += saved
	s.file.Totals.TokensReturned += returned
	s.file.Totals.CallsCounted++
	s.file.LastUpdated = time.Now().UTC()

	if repoPath != "" {
		t := s.file.PerRepo[repoPath]
		if t == nil {
			t = &Totals{}
			s.file.PerRepo[repoPath] = t
		}
		t.TokensSaved += saved
		t.TokensReturned += returned
		t.CallsCounted++
	}

	s.pending++
	if s.pending >= flushEvery {
		_ = s.flushLocked()
	}
}

// Snapshot returns a deep copy of the current totals (safe for reads outside
// the mutex). Used by graph_stats and the CLI.
func (s *Store) Snapshot() File {
	if s == nil {
		return File{Version: schemaVersion, PerRepo: map[string]*Totals{}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	cp := s.file
	cp.PerRepo = make(map[string]*Totals, len(s.file.PerRepo))
	for k, v := range s.file.PerRepo {
		t := *v
		cp.PerRepo[k] = &t
	}
	return cp
}

// Flush writes pending observations to disk. Safe to call when no path is
// configured (no-op).
func (s *Store) Flush() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushLocked()
}

// Reset wipes all cumulative data and removes the persisted file. Used by
// `gortex savings --reset`.
func (s *Store) Reset() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.file = File{
		Version:   schemaVersion,
		FirstSeen: time.Now().UTC(),
		PerRepo:   make(map[string]*Totals),
	}
	s.pending = 0

	if s.path == "" {
		return nil
	}
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// flushLocked must be called with s.mu held.
func (s *Store) flushLocked() error {
	if s.path == "" {
		s.pending = 0
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}

	// Atomic write: temp file in the same dir, then rename.
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".savings-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s.file); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	s.pending = 0
	return nil
}
