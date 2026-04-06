package indexer

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
	"github.com/zzet/gortex/internal/parser/languages"
)

func setupWatcher(t *testing.T) (string, *Indexer, *Watcher) {
	t.Helper()
	dir := t.TempDir()

	writeTestFile(t, filepath.Join(dir, "main.go"), `package main

func Original() {}
`)

	g := graph.New()
	reg := parser.NewRegistry()
	reg.Register(languages.NewGoExtractor())
	cfg := config.Default()
	cfg.Index.Workers = 1

	idx := New(g, reg, cfg.Index, zap.NewNop())
	_, err := idx.Index(dir)
	require.NoError(t, err)

	wcfg := config.WatchConfig{
		Enabled:    true,
		Paths:      []string{dir},
		DebounceMs: 50, // short debounce for tests
		Exclude:    []string{"**/*.tmp", "**/.git/**"},
	}

	w, err := NewWatcher(idx, wcfg, zap.NewNop())
	require.NoError(t, err)
	require.NoError(t, w.Start([]string{dir}))

	t.Cleanup(func() { _ = w.Stop() })
	return dir, idx, w
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func waitForEvent(t *testing.T, w *Watcher, timeout time.Duration) GraphChangeEvent {
	t.Helper()
	select {
	case ev := <-w.Events():
		return ev
	case <-time.After(timeout):
		t.Fatal("timeout waiting for watcher event")
		return GraphChangeEvent{}
	}
}

func TestWatcher_FileModify(t *testing.T) {
	dir, idx, w := setupWatcher(t)

	require.NotEmpty(t, idx.graph.FindNodesByName("Original"))

	// Modify the file.
	writeTestFile(t, filepath.Join(dir, "main.go"), `package main

func Modified() {}
`)

	ev := waitForEvent(t, w, 2*time.Second)
	assert.Equal(t, ChangeModified, ev.Kind)

	// Graph should reflect the change.
	assert.Empty(t, idx.graph.FindNodesByName("Original"))
	assert.NotEmpty(t, idx.graph.FindNodesByName("Modified"))
}

func TestWatcher_FileCreate(t *testing.T) {
	dir, idx, w := setupWatcher(t)

	nodesBefore := idx.graph.NodeCount()

	writeTestFile(t, filepath.Join(dir, "new.go"), `package main

func NewFunc() {}
`)

	ev := waitForEvent(t, w, 2*time.Second)
	// fsnotify may emit CREATE or WRITE depending on the OS.
	assert.Contains(t, []ChangeKind{ChangeCreated, ChangeModified}, ev.Kind)
	assert.Greater(t, idx.graph.NodeCount(), nodesBefore)
	assert.NotEmpty(t, idx.graph.FindNodesByName("NewFunc"))
}

func TestWatcher_FileDelete(t *testing.T) {
	dir, idx, w := setupWatcher(t)

	require.NotEmpty(t, idx.graph.FindNodesByName("Original"))

	require.NoError(t, os.Remove(filepath.Join(dir, "main.go")))

	ev := waitForEvent(t, w, 2*time.Second)
	assert.Equal(t, ChangeDeleted, ev.Kind)
	assert.Empty(t, idx.graph.FindNodesByName("Original"))
}

func TestWatcher_History(t *testing.T) {
	dir, _, w := setupWatcher(t)

	writeTestFile(t, filepath.Join(dir, "main.go"), `package main

func Changed() {}
`)
	_ = waitForEvent(t, w, 2*time.Second)

	history := w.History()
	require.Len(t, history, 1)
	assert.Equal(t, ChangeModified, history[0].Kind)
}
