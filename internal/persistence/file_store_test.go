package persistence

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

func testSnapshot() *Snapshot {
	return &Snapshot{
		Version:    "0.1.0-test",
		RepoPath:   "/tmp/test-repo",
		CommitHash: "abc123def456",
		Branch:     "main",
		IndexedAt:  time.Now().Truncate(time.Second),
		Nodes: []*graph.Node{
			{
				ID: "main.go::Foo", Kind: graph.KindFunction, Name: "Foo",
				FilePath: "main.go", StartLine: 1, EndLine: 5, Language: "go",
				Meta: map[string]any{"signature": "func Foo(x int) error"},
			},
			{
				ID: "main.go::Bar", Kind: graph.KindMethod, Name: "Bar",
				FilePath: "main.go", StartLine: 7, EndLine: 12, Language: "go",
				Meta: map[string]any{"receiver": "Server", "signature": "func (s *Server) Bar()"},
			},
		},
		Edges: []*graph.Edge{
			{
				From: "main.go::Foo", To: "main.go::Bar", Kind: graph.EdgeCalls,
				FilePath: "main.go", Line: 3, Confidence: 0.95,
				Meta: map[string]any{"receiver_type": "Server"},
			},
		},
		FileMtimes: map[string]int64{
			"main.go": 1700000000000000000,
			"util.go": 1700000001000000000,
		},
	}
}

func TestFileStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileStore(dir, "0.1.0-test")
	require.NoError(t, err)

	snap := testSnapshot()

	require.NoError(t, fs.Save(snap))
	assert.True(t, fs.Check(snap.RepoPath, snap.Branch, snap.CommitHash))
	assert.True(t, fs.Validate(snap.RepoPath, snap.Branch, snap.CommitHash))

	loaded, err := fs.Load(snap.RepoPath, snap.Branch, snap.CommitHash)
	require.NoError(t, err)

	assert.Equal(t, snap.Version, loaded.Version)
	assert.Equal(t, snap.RepoPath, loaded.RepoPath)
	assert.Equal(t, snap.CommitHash, loaded.CommitHash)
	assert.Equal(t, snap.Branch, loaded.Branch)
	assert.Equal(t, snap.IndexedAt, loaded.IndexedAt)

	require.Len(t, loaded.Nodes, 2)
	assert.Equal(t, "main.go::Foo", loaded.Nodes[0].ID)
	assert.Equal(t, "Foo", loaded.Nodes[0].Name)
	assert.Equal(t, "func Foo(x int) error", loaded.Nodes[0].Meta["signature"])

	assert.Equal(t, "Server", loaded.Nodes[1].Meta["receiver"])

	require.Len(t, loaded.Edges, 1)
	assert.Equal(t, "main.go::Foo", loaded.Edges[0].From)
	assert.Equal(t, "main.go::Bar", loaded.Edges[0].To)
	assert.Equal(t, 0.95, loaded.Edges[0].Confidence)
	assert.Equal(t, "Server", loaded.Edges[0].Meta["receiver_type"])

	assert.Equal(t, snap.FileMtimes, loaded.FileMtimes)
}

func TestFileStore_Validate_VersionMismatch(t *testing.T) {
	dir := t.TempDir()
	fsV1, err := NewFileStore(dir, "0.1.0")
	require.NoError(t, err)

	snap := testSnapshot()
	snap.Version = "0.1.0"
	require.NoError(t, fsV1.Save(snap))

	// Same version validates.
	assert.True(t, fsV1.Validate(snap.RepoPath, snap.Branch, snap.CommitHash))

	// A different BINARY version now REUSES the slot — the extraction version
	// (not the binary string) gates reuse, so a no-op release skips the rebuild.
	fsV2, err := NewFileStore(dir, "0.2.0")
	require.NoError(t, err)
	assert.True(t, fsV2.Validate(snap.RepoPath, snap.Branch, snap.CommitHash))

	// A LEGACY slot with no extraction-version marker falls back to the exact
	// binary-version match — so it correctly invalidates across the bump.
	entry := fsV2.entryDir(snap.RepoPath, snap.Branch, snap.CommitHash)
	require.NoError(t, os.Remove(filepath.Join(entry, extractionVersionFile)))
	assert.False(t, fsV2.Validate(snap.RepoPath, snap.Branch, snap.CommitHash))
}

func TestFileStore_Evict(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileStore(dir, "0.1.0")
	require.NoError(t, err)

	snap := testSnapshot()
	require.NoError(t, fs.Save(snap))
	assert.True(t, fs.Check(snap.RepoPath, snap.Branch, snap.CommitHash))

	require.NoError(t, fs.Evict(snap.RepoPath, snap.Branch, snap.CommitHash))
	assert.False(t, fs.Check(snap.RepoPath, snap.Branch, snap.CommitHash))
}

func TestFileStore_Load_NotFound(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileStore(dir, "0.1.0")
	require.NoError(t, err)

	_, err = fs.Load("/nonexistent", "main", "abc123")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestFileStore_MetaWithSliceTypes(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileStore(dir, "0.1.0")
	require.NoError(t, err)

	snap := &Snapshot{
		Version:    "0.1.0",
		RepoPath:   "/tmp/test",
		CommitHash: "def789",
		Branch:     "main",
		IndexedAt:  time.Now().Truncate(time.Second),
		Nodes: []*graph.Node{
			{
				ID: "iface.go::Reader", Kind: graph.KindInterface, Name: "Reader",
				FilePath: "iface.go", Language: "go",
				Meta: map[string]any{"methods": []string{"Read", "Close"}},
			},
		},
		FileMtimes: map[string]int64{"iface.go": 1700000000},
	}

	require.NoError(t, fs.Save(snap))

	loaded, err := fs.Load(snap.RepoPath, snap.Branch, snap.CommitHash)
	require.NoError(t, err)

	methods, ok := loaded.Nodes[0].Meta["methods"].([]string)
	require.True(t, ok, "methods should deserialize as []string")
	assert.Equal(t, []string{"Read", "Close"}, methods)
}

// TestFileStore_BranchKeyedSlots proves snapshots are keyed by
// (repo, branch): two branches of the same repo, even at the same
// commit, occupy distinct slots, so switching branches never clobbers
// the other branch's cached index.
func TestFileStore_BranchKeyedSlots(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileStore(dir, "0.1.0-test")
	require.NoError(t, err)

	main := testSnapshot()
	main.Branch = "main"
	feature := testSnapshot()
	feature.Branch = "feature/login"
	feature.Nodes[0].Name = "FeatureFoo"

	require.NoError(t, fs.Save(main))
	require.NoError(t, fs.Save(feature))

	gotMain, err := fs.Load(main.RepoPath, "main", main.CommitHash)
	require.NoError(t, err)
	assert.Equal(t, "Foo", gotMain.Nodes[0].Name)

	gotFeature, err := fs.Load(feature.RepoPath, "feature/login", feature.CommitHash)
	require.NoError(t, err)
	assert.Equal(t, "FeatureFoo", gotFeature.Nodes[0].Name)
}

// TestFileStore_DetachedHeadKeyedByCommit checks the detached-HEAD
// fallback: with no branch the slot keys on the commit hash, so two
// checked-out commits keep separate snapshots.
func TestFileStore_DetachedHeadKeyedByCommit(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileStore(dir, "0.1.0-test")
	require.NoError(t, err)

	a := testSnapshot()
	a.Branch = ""
	a.CommitHash = "aaaaaaaaaaaa"
	b := testSnapshot()
	b.Branch = ""
	b.CommitHash = "bbbbbbbbbbbb"

	require.NoError(t, fs.Save(a))
	require.NoError(t, fs.Save(b))

	assert.True(t, fs.Check(a.RepoPath, "", "aaaaaaaaaaaa"))
	assert.True(t, fs.Check(b.RepoPath, "", "bbbbbbbbbbbb"))
}

func TestNopStore(t *testing.T) {
	var s NopStore
	assert.False(t, s.Check("x", "main", "y"))
	_, err := s.Load("x", "main", "y")
	assert.ErrorIs(t, err, ErrNotFound)
	assert.NoError(t, s.Save(testSnapshot()))
	assert.False(t, s.Validate("x", "main", "y"))
	assert.NoError(t, s.Evict("x", "main", "y"))
	assert.NoError(t, s.Close())
}

// TestFileStore_ConcurrentSave exercises the cross-process advisory lock:
// every writer targets the same cache key, so without serialization
// one writer's os.RemoveAll would race another's MkdirAll/write sequence
// and leave a torn entry. flock(2) contends across file descriptors even
// within one process, so concurrent goroutines reproduce the cross-process
// hazard. After all writers complete, the entry must load as exactly one
// writer's complete payload.
func TestFileStore_ConcurrentSave(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileStore(dir, "0.1.0-test")
	require.NoError(t, err)

	const writers = 12
	markers := make(map[string]bool, writers)
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range writers {
		marker := fmt.Sprintf("writer-%d", i)
		markers[marker] = true
		wg.Add(1)
		go func(m string) {
			defer wg.Done()
			snap := testSnapshot()
			snap.Nodes[0].Meta["writer"] = m
			errs <- fs.Save(snap)
		}(marker)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}

	loaded, err := fs.Load("/tmp/test-repo", "main", "abc123def456")
	require.NoError(t, err)
	require.Len(t, loaded.Nodes, 2)
	got, _ := loaded.Nodes[0].Meta["writer"].(string)
	assert.True(t, markers[got], "loaded snapshot must be one writer's complete payload, got %q", got)
}

// TestFileStore_ConcurrentReadWrite runs readers against a writer churning
// the same entry. The shared read lock must hand every reader either a
// fully decodable snapshot or a clean ErrNotFound — never a gob/gzip
// decode error from a half-written file.
func TestFileStore_ConcurrentReadWrite(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileStore(dir, "0.1.0-test")
	require.NoError(t, err)

	snap := testSnapshot()
	require.NoError(t, fs.Save(snap))

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			var e error
			if i%2 == 0 {
				e = fs.Save(snap)
			} else {
				e = fs.Evict(snap.RepoPath, snap.Branch, snap.CommitHash)
			}
			// Honour stop while sending: errs is buffered, and the
			// writer outruns the buffer in microseconds. Without the
			// stop arm here the writer blocks on a full errs channel,
			// never re-checks stop, and wg.Wait() deadlocks (the buffer
			// only drains after wg.Wait()).
			select {
			case errs <- e:
			case <-stop:
				return
			}
		}
	}()

	for r := 0; r < 6; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				_, err := fs.Load(snap.RepoPath, snap.Branch, snap.CommitHash)
				if err != nil && err != ErrNotFound {
					errs <- err
				}
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e, "no reader may observe a torn snapshot")
	}
}

// TestSnapshotReuseAcrossBinaryBump proves the warm snapshot is reused across a
// binary version bump that did NOT change extraction output (the extraction
// version is the gate, not the binary-version string) — avoiding the needless
// full cold rebuild a binary-string gate would force on a no-op release — while
// a genuine extraction-version change still invalidates the slot.
func TestSnapshotReuseAcrossBinaryBump(t *testing.T) {
	dir := t.TempDir()

	s1, err := NewFileStore(dir, "0.48.0")
	if err != nil {
		t.Fatal(err)
	}
	snap := testSnapshot()
	snap.Version = "0.48.0"
	if err := s1.Save(snap); err != nil {
		t.Fatal(err)
	}

	// A newer binary with the SAME extraction version reuses the slot.
	s2, err := NewFileStore(dir, "0.48.1")
	if err != nil {
		t.Fatal(err)
	}
	if !s2.Validate(snap.RepoPath, snap.Branch, snap.CommitHash) {
		t.Error("snapshot should be reused across a binary bump with an unchanged extraction version")
	}

	// Corrupt the extraction-version marker to simulate an extraction-output
	// change: the slot must now be rejected (cold rebuild).
	entry := s2.entryDir(snap.RepoPath, snap.Branch, snap.CommitHash)
	if err := os.WriteFile(filepath.Join(entry, extractionVersionFile), []byte("999"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s2.Validate(snap.RepoPath, snap.Branch, snap.CommitHash) {
		t.Error("a changed extraction version must invalidate the snapshot")
	}
}
