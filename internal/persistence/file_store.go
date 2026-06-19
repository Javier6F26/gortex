package persistence

import (
	"compress/gzip"
	"encoding/gob"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gofrs/flock"

	"github.com/zzet/gortex/internal/platform"
)

func init() {
	// Register concrete types that appear in Node.Meta / Edge.Meta map[string]any.
	gob.Register(map[string]any{})
	gob.Register([]any{})
	gob.Register([]string{})
	gob.Register([]int{})
	gob.Register([]map[string]string{})
	gob.Register([]map[string]any{})
}

const (
	snapshotFile          = "snapshot.gob.gz"
	versionFile           = ".version"
	extractionVersionFile = ".extraction_version"
)

// FileStore persists snapshots as gob+gzip files in a directory hierarchy.
// Layout: {dir}/{cacheKey}/snapshot.gob.gz + .version
type FileStore struct {
	dir     string
	version string
}

// NewFileStore creates a file-based persistence store.
// If dir is empty, defaults to the Gortex cache dir (~/.gortex/cache/,
// or the $XDG_CACHE_HOME equivalent when that variable is set).
func NewFileStore(dir, version string) (*FileStore, error) {
	if dir == "" {
		dir = platform.CacheDir()
	}
	return &FileStore{dir: dir, version: version}, nil
}

func (fs *FileStore) entryDir(repoPath, branch, commitHash string) string {
	return filepath.Join(fs.dir, CacheKey(repoPath, branch, commitHash))
}

// lockPath is the advisory-lock file for one snapshot entry. It is a
// sibling of the entry directory ({dir}/{cacheKey}.lock), deliberately
// outside it so the lock survives the os.RemoveAll a writer runs against
// the entry directory on Save/Evict.
func (fs *FileStore) lockPath(repoPath, branch, commitHash string) string {
	return fs.entryDir(repoPath, branch, commitHash) + ".lock"
}

// acquireWrite takes an exclusive cross-process advisory lock for one
// snapshot entry. A second gortex process writing the same snapshot
// blocks here instead of racing the RemoveAll/MkdirAll/write sequence,
// which would otherwise leave a torn or empty index on disk.
func (fs *FileStore) acquireWrite(repoPath, branch, commitHash string) (*flock.Flock, error) {
	if err := os.MkdirAll(fs.dir, 0o755); err != nil {
		return nil, fmt.Errorf("persistence: mkdir cache dir: %w", err)
	}
	fl := flock.New(fs.lockPath(repoPath, branch, commitHash))
	if err := fl.Lock(); err != nil {
		return nil, fmt.Errorf("persistence: acquire index write lock: %w", err)
	}
	return fl, nil
}

// acquireRead takes a shared cross-process advisory lock for one snapshot
// entry, so a reader waits out an in-progress write instead of decoding a
// half-written snapshot.
func (fs *FileStore) acquireRead(repoPath, branch, commitHash string) (*flock.Flock, error) {
	if err := os.MkdirAll(fs.dir, 0o755); err != nil {
		return nil, fmt.Errorf("persistence: mkdir cache dir: %w", err)
	}
	fl := flock.New(fs.lockPath(repoPath, branch, commitHash))
	if err := fl.RLock(); err != nil {
		return nil, fmt.Errorf("persistence: acquire index read lock: %w", err)
	}
	return fl, nil
}

func (fs *FileStore) Check(repoPath, branch, commitHash string) bool {
	dir := fs.entryDir(repoPath, branch, commitHash)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, versionFile))
	return err == nil
}

func (fs *FileStore) Validate(repoPath, branch, commitHash string) bool {
	dir := fs.entryDir(repoPath, branch, commitHash)
	// Extraction-version gate: a warm snapshot is reusable across binary
	// releases that produce the SAME extraction output, so a no-op version
	// bump no longer forces a full cold rebuild.
	if data, err := os.ReadFile(filepath.Join(dir, extractionVersionFile)); err == nil {
		v, perr := strconv.Atoi(strings.TrimSpace(string(data)))
		return perr == nil && v == CurrentExtractionVersion
	}
	// Legacy slot with no extraction-version file: fall back to the exact
	// binary-version match so pre-existing snapshots still validate (and get
	// rewritten with an extraction-version marker on the next save).
	data, err := os.ReadFile(filepath.Join(dir, versionFile))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == fs.version
}

func (fs *FileStore) Load(repoPath, branch, commitHash string) (*Snapshot, error) {
	fl, err := fs.acquireRead(repoPath, branch, commitHash)
	if err != nil {
		return nil, err
	}
	defer func() { _ = fl.Unlock() }()

	dir := fs.entryDir(repoPath, branch, commitHash)
	f, err := os.Open(filepath.Join(dir, snapshotFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("persistence: open snapshot: %w", err)
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("persistence: gzip reader: %w", err)
	}
	defer func() { _ = gz.Close() }()

	var snap Snapshot
	if err := gob.NewDecoder(gz).Decode(&snap); err != nil {
		return nil, fmt.Errorf("persistence: gob decode: %w", err)
	}

	return &snap, nil
}

func (fs *FileStore) Save(snap *Snapshot) error {
	fl, err := fs.acquireWrite(snap.RepoPath, snap.Branch, snap.CommitHash)
	if err != nil {
		return err
	}
	defer func() { _ = fl.Unlock() }()

	dir := fs.entryDir(snap.RepoPath, snap.Branch, snap.CommitHash)

	// Remove old entry if it exists.
	_ = os.RemoveAll(dir)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("persistence: mkdir: %w", err)
	}

	// Write snapshot.
	f, err := os.Create(filepath.Join(dir, snapshotFile))
	if err != nil {
		return fmt.Errorf("persistence: create snapshot: %w", err)
	}

	gz := gzip.NewWriter(f)
	enc := gob.NewEncoder(gz)

	if err := enc.Encode(snap); err != nil {
		_ = gz.Close()
		_ = f.Close()
		return fmt.Errorf("persistence: gob encode: %w", err)
	}

	if err := gz.Close(); err != nil {
		_ = f.Close()
		return fmt.Errorf("persistence: gzip close: %w", err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("persistence: file close: %w", err)
	}

	// Write version file (binary version — kept for display / diagnostics).
	if err := os.WriteFile(filepath.Join(dir, versionFile), []byte(fs.version), 0o644); err != nil {
		return fmt.Errorf("persistence: write version: %w", err)
	}
	// Write the extraction-version marker — the value Validate gates reuse on.
	if err := os.WriteFile(filepath.Join(dir, extractionVersionFile), []byte(strconv.Itoa(CurrentExtractionVersion)), 0o644); err != nil {
		return fmt.Errorf("persistence: write extraction version: %w", err)
	}

	return nil
}

func (fs *FileStore) Evict(repoPath, branch, commitHash string) error {
	fl, err := fs.acquireWrite(repoPath, branch, commitHash)
	if err != nil {
		return err
	}
	defer func() { _ = fl.Unlock() }()
	return os.RemoveAll(fs.entryDir(repoPath, branch, commitHash))
}

func (fs *FileStore) Close() error { return nil }
