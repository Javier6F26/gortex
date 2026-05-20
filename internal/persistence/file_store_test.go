package persistence

import (
	"fmt"
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

	// Save.
	require.NoError(t, fs.Save(snap))

	// Check.
	assert.True(t, fs.Check(snap.RepoPath, snap.CommitHash))

	// Validate.
	assert.True(t, fs.Validate(snap.RepoPath, snap.CommitHash))

	// Load.
	loaded, err := fs.Load(snap.RepoPath, snap.CommitHash)
	require.NoError(t, err)

	// Verify.
	assert.Equal(t, snap.Version, loaded.Version)
	assert.Equal(t, snap.RepoPath, loaded.RepoPath)
	assert.Equal(t, snap.CommitHash, loaded.CommitHash)
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
	assert.True(t, fsV1.Validate(snap.RepoPath, snap.CommitHash))

	// Different version fails.
	fsV2, err := NewFileStore(dir, "0.2.0")
	require.NoError(t, err)
	assert.False(t, fsV2.Validate(snap.RepoPath, snap.CommitHash))
}

func TestFileStore_Evict(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileStore(dir, "0.1.0")
	require.NoError(t, err)

	snap := testSnapshot()
	require.NoError(t, fs.Save(snap))
	assert.True(t, fs.Check(snap.RepoPath, snap.CommitHash))

	require.NoError(t, fs.Evict(snap.RepoPath, snap.CommitHash))
	assert.False(t, fs.Check(snap.RepoPath, snap.CommitHash))
}

func TestFileStore_Load_NotFound(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileStore(dir, "0.1.0")
	require.NoError(t, err)

	_, err = fs.Load("/nonexistent", "abc123")
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

	loaded, err := fs.Load(snap.RepoPath, snap.CommitHash)
	require.NoError(t, err)

	methods, ok := loaded.Nodes[0].Meta["methods"].([]string)
	require.True(t, ok, "methods should deserialize as []string")
	assert.Equal(t, []string{"Read", "Close"}, methods)
}

func TestNopStore(t *testing.T) {
	var s NopStore
	assert.False(t, s.Check("x", "y"))
	_, err := s.Load("x", "y")
	assert.ErrorIs(t, err, ErrNotFound)
	assert.NoError(t, s.Save(testSnapshot()))
	assert.False(t, s.Validate("x", "y"))
	assert.NoError(t, s.Evict("x", "y"))
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

	loaded, err := fs.Load("/tmp/test-repo", "abc123def456")
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
			if i%2 == 0 {
				errs <- fs.Save(snap)
			} else {
				errs <- fs.Evict(snap.RepoPath, snap.CommitHash)
			}
		}
	}()

	for r := 0; r < 6; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				_, err := fs.Load(snap.RepoPath, snap.CommitHash)
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
