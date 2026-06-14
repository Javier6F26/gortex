package indexer

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/search"
)

// TestPollInterval_ScalesWithProjectSize is the core contract of the
// adaptive poller: a small repo polls often, a large repo polls
// rarely. The interval must be monotonically non-decreasing in the
// node count so adding code never makes the fallback more aggressive.
func TestPollInterval_ScalesWithProjectSize(t *testing.T) {
	small := pollInterval(100)              // a tiny service
	medium := pollInterval(20 * 1000)       // a mid-size repo
	large := pollInterval(500 * 1000)       // a large monorepo
	huge := pollInterval(50 * 1000 * 1000)  // absurdly large

	assert.LessOrEqual(t, small, medium,
		"a small repo must not poll less often than a medium one")
	assert.LessOrEqual(t, medium, large,
		"a medium repo must not poll less often than a large one")
	assert.Less(t, small, large,
		"a small repo must poll strictly more often than a large one")
	assert.Equal(t, huge, large,
		"the interval saturates — beyond the ceiling, more nodes don't widen it")
}

// TestPollInterval_Bounds proves the interval is clamped: it never
// drops below the floor (so the fallback can't become a hot loop on a
// tiny repo) and never rises above the ceiling (so a huge repo is
// still swept periodically rather than effectively never).
func TestPollInterval_Bounds(t *testing.T) {
	cases := []struct {
		name      string
		nodeCount int
	}{
		{"empty_repo", 0},
		{"negative_guard", -5},
		{"one_node", 1},
		{"tiny", 50},
		{"mid", 100 * 1000},
		{"enormous", 1 << 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := pollInterval(tc.nodeCount)
			assert.GreaterOrEqual(t, d, pollIntervalMin,
				"interval must not drop below the floor")
			assert.LessOrEqual(t, d, pollIntervalMax,
				"interval must not rise above the ceiling")
		})
	}
}

// TestPollInterval_DerivedFromRealSignal checks the interval is a
// genuine function of the indexed node count — wiring newPoller to a
// graph with more nodes must produce an interval at least as long as
// one wired to a near-empty graph.
func TestPollInterval_DerivedFromRealSignal(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc Only() {}\n")

	g := graph.New()
	idx := newTestIndexer(g)
	idx.SetRootPath(dir)
	_, err := idx.Index(dir)
	require.NoError(t, err)

	w, err := NewWatcher(idx, config.WatchConfig{Enabled: true, DebounceMs: 10}, zap.NewNop())
	require.NoError(t, err)

	p := newPoller(w, idx, zap.NewNop())
	// A near-empty repo sits at (or very close to) the floor.
	assert.Equal(t, pollInterval(g.NodeCount()), p.interval,
		"poller interval must be derived from the indexed node count")
	assert.GreaterOrEqual(t, p.interval, pollIntervalMin)
}

// TestPoller_DetectsFilesystemChangeMissedByFsnotify proves the
// fallback works: a tracked file is modified on disk and the poll
// cycle — not fsnotify — re-indexes it. The poll is driven directly
// so the test does not depend on the (deliberately long) adaptive
// interval elapsing.
func TestPoller_DetectsFilesystemChangeMissedByFsnotify(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	writeTestFile(t, path, "package main\n\nfunc Before() {}\n")

	g := graph.New()
	idx := newTestIndexer(g)
	idx.search = search.NewBM25()
	idx.SetRootPath(dir)
	_, err := idx.Index(dir)
	require.NoError(t, err)
	require.NotEmpty(t, g.FindNodesByName("Before"))

	w, err := NewWatcher(idx, config.WatchConfig{Enabled: true, DebounceMs: 10}, zap.NewNop())
	require.NoError(t, err)
	p := newPoller(w, idx, zap.NewNop())

	// Rewrite the file with an mtime strictly after the indexed one.
	// This is the change a missed fsnotify event would have left
	// invisible to the graph.
	future := time.Now().Add(2 * time.Second)
	writeTestFile(t, path, "package main\n\nfunc After() {}\n")
	require.NoError(t, os.Chtimes(path, future, future))

	// One poll cycle must notice the advanced mtime and re-index.
	p.poll()

	assert.Empty(t, g.FindNodesByName("Before"),
		"the stale symbol must be evicted by the poll cycle")
	assert.NotEmpty(t, g.FindNodesByName("After"),
		"the poll cycle must re-index a file fsnotify missed")
}

// TestPoller_DetectsDeletedFileMissedByFsnotify covers the delete
// half of the filesystem fallback: a tracked file vanishes from disk
// and the poll cycle evicts it even though no fsnotify remove event
// arrived.
func TestPoller_DetectsDeletedFileMissedByFsnotify(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gone.go")
	writeTestFile(t, path, "package main\n\nfunc Gone() {}\n")

	g := graph.New()
	idx := newTestIndexer(g)
	idx.search = search.NewBM25()
	idx.SetRootPath(dir)
	_, err := idx.Index(dir)
	require.NoError(t, err)
	require.NotEmpty(t, g.FindNodesByName("Gone"))

	w, err := NewWatcher(idx, config.WatchConfig{Enabled: true, DebounceMs: 10}, zap.NewNop())
	require.NoError(t, err)
	p := newPoller(w, idx, zap.NewNop())

	require.NoError(t, os.Remove(path))
	p.poll()

	assert.Empty(t, g.FindNodesByName("Gone"),
		"the poll cycle must evict a file deleted while fsnotify missed it")
}

// TestPoller_DetectsGitHeadMoveMissedByFsnotify is the end-to-end
// proof for the git-HEAD half of the fallback: HEAD moves to a branch
// with different file content and the poll cycle reconciles the graph
// to the new commit without the GitWatcher's fsnotify watch firing.
func TestPoller_DetectsGitHeadMoveMissedByFsnotify(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available in PATH")
	}

	repoDir := t.TempDir()
	runGit(t, repoDir, "init", "-q", "-b", "main")
	runGit(t, repoDir, "config", "user.email", "test@example.com")
	runGit(t, repoDir, "config", "user.name", "Test")
	runGit(t, repoDir, "config", "commit.gpgsign", "false")

	writeFile(t, filepath.Join(repoDir, "a.go"), "package main\nfunc Alpha() {}\n")
	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "commit", "-q", "-m", "main: Alpha")

	// Build a feature branch that replaces a.go with b.go.
	runGit(t, repoDir, "checkout", "-q", "-b", "feature")
	require.NoError(t, os.Remove(filepath.Join(repoDir, "a.go")))
	writeFile(t, filepath.Join(repoDir, "b.go"), "package main\nfunc Beta() {}\n")
	runGit(t, repoDir, "add", "-A")
	runGit(t, repoDir, "commit", "-q", "-m", "feature: Beta")
	runGit(t, repoDir, "checkout", "-q", "main")

	g := graph.New()
	idx := New(g, newTestRegistry(), config.IndexConfig{Workers: 1}, zap.NewNop())
	idx.search = search.NewBM25()
	idx.SetRootPath(repoDir)
	_, err := idx.IndexCtx(testCtx(), repoDir)
	require.NoError(t, err)
	require.NotEmpty(t, g.GetFileNodes("a.go"))
	require.Empty(t, g.GetFileNodes("b.go"))

	w, err := NewWatcher(idx, config.WatchConfig{Enabled: true, DebounceMs: 10}, zap.NewNop())
	require.NoError(t, err)
	// newPoller records the HEAD SHA at construction. After it,
	// switch branches — the poll cycle sees the SHA differ.
	p := newPoller(w, idx, zap.NewNop())
	require.NotEmpty(t, p.lastSHA, "poller must capture the starting HEAD SHA")

	runGit(t, repoDir, "checkout", "-q", "feature")
	p.poll()

	assert.Empty(t, g.GetFileNodes("a.go"),
		"after the poll reconcile, Alpha's file must be evicted")
	assert.NotEmpty(t, g.GetFileNodes("b.go"),
		"the poll cycle must index the feature branch's Beta")
}

// TestPoller_RespectsWatcherDisableKnob verifies the poller honours
// the per-repo watcher-disable knob: when WatchConfig.Enabled is
// false, Start must not create a poller — the disabled repo gets no
// fallback either.
func TestPoller_RespectsWatcherDisableKnob(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc Main() {}\n")

	g := graph.New()
	idx := newTestIndexer(g)
	idx.SetRootPath(dir)
	_, err := idx.Index(dir)
	require.NoError(t, err)

	w, err := NewWatcher(idx, config.WatchConfig{Enabled: false, DebounceMs: 10}, zap.NewNop())
	require.NoError(t, err)
	require.NoError(t, w.Start([]string{dir}))
	t.Cleanup(func() { _ = w.Stop() })

	assert.Nil(t, w.poller,
		"a watcher started with Enabled=false must not run an adaptive poller")
}

// TestPoller_StartedAndStoppedWithWatcher proves the poller shares
// the watcher lifecycle: Start brings it up, Stop tears it down, and
// the whole sequence is deadlock-free.
func TestPoller_StartedAndStoppedWithWatcher(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc Main() {}\n")

	g := graph.New()
	idx := newTestIndexer(g)
	idx.SetRootPath(dir)
	_, err := idx.Index(dir)
	require.NoError(t, err)

	w, err := NewWatcher(idx, config.WatchConfig{Enabled: true, DebounceMs: 10}, zap.NewNop())
	require.NoError(t, err)
	require.NoError(t, w.Start([]string{dir}))
	require.NotNil(t, w.poller, "an enabled watcher must run an adaptive poller")

	done := make(chan struct{})
	go func() {
		_ = w.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("watcher Stop deadlocked tearing down the poller")
	}
}

// TestPoller_StopIdempotent guards the teardown path: Stop must be
// safe whether Start launched the loop, was a no-op (no indexer /
// root), or Stop was already called once.
func TestPoller_StopIdempotent(t *testing.T) {
	// Inert poller — no indexer, Start is a no-op.
	inert := &Poller{done: make(chan struct{})}
	inert.Start()
	inert.Stop()
	inert.Stop() // second call must not panic or block

	// Live poller — Start launches the loop, Stop joins it.
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc Main() {}\n")
	g := graph.New()
	idx := newTestIndexer(g)
	idx.SetRootPath(dir)
	_, err := idx.Index(dir)
	require.NoError(t, err)

	w, err := NewWatcher(idx, config.WatchConfig{Enabled: true}, zap.NewNop())
	require.NoError(t, err)
	p := newPoller(w, idx, zap.NewNop())
	p.Start()

	done := make(chan struct{})
	go func() {
		p.Stop()
		p.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("poller Stop deadlocked")
	}
}

// TestPoller_NotifyFileTriggersReconcile verifies touching the notify file
// forces an immediate reconcile via the fast notify loop, independent of the
// (much longer) adaptive poll interval.
func TestPoller_NotifyFileTriggersReconcile(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc Main() {}\n")

	g := graph.New()
	idx := newTestIndexer(g)
	idx.SetRootPath(dir)
	_, err := idx.Index(dir)
	require.NoError(t, err)

	w, err := NewWatcher(idx, config.WatchConfig{Enabled: true, DebounceMs: 10}, zap.NewNop())
	require.NoError(t, err)
	p := newPoller(w, idx, zap.NewNop())
	require.NotEmpty(t, p.notifyPath, "notify path must default under .gortex")
	require.NoError(t, os.MkdirAll(filepath.Dir(p.notifyPath), 0o755))
	require.NoError(t, os.WriteFile(p.notifyPath, []byte("x"), 0o644))

	swept := make(chan int, 8)
	p.swept = func(n int) { swept <- n }
	p.Start()
	defer p.Stop()

	// Touch the notify file with a strictly later mtime.
	time.Sleep(20 * time.Millisecond)
	future := time.Now().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(p.notifyPath, future, future))

	select {
	case <-swept:
		// the notify loop forced a reconcile within the fast cadence
	case <-time.After(3 * time.Second):
		t.Fatal("notify-file touch did not trigger a reconcile within 3s")
	}
}

// TestPoller_SweepHookReportsWork exercises the swept test hook and
// confirms a poll cycle reports the number of files it re-dispatched
// — the same count the production logger emits.
func TestPoller_SweepHookReportsWork(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	writeTestFile(t, path, "package main\n\nfunc Before() {}\n")

	g := graph.New()
	idx := newTestIndexer(g)
	idx.search = search.NewBM25()
	idx.SetRootPath(dir)
	_, err := idx.Index(dir)
	require.NoError(t, err)

	w, err := NewWatcher(idx, config.WatchConfig{Enabled: true, DebounceMs: 10}, zap.NewNop())
	require.NoError(t, err)
	p := newPoller(w, idx, zap.NewNop())

	var swept int
	p.swept = func(n int) { swept = n }

	// No change yet — a clean sweep reports zero work.
	p.poll()
	assert.Equal(t, 0, swept, "a quiet poll cycle must report no work")

	// Now mutate the file and sweep again.
	future := time.Now().Add(2 * time.Second)
	writeTestFile(t, path, "package main\n\nfunc After() {}\n")
	require.NoError(t, os.Chtimes(path, future, future))
	p.poll()
	assert.Equal(t, 1, swept, "the poll cycle must report the one file it re-indexed")
}
