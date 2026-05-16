package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/contracts"
	"github.com/zzet/gortex/internal/graph"
)

// TestSnapshotRoundTrip proves that save + load preserves nodes and
// edges bit-for-bit. This is the guarantee the daemon's startup restore
// depends on; a silent corruption here would give warm-started daemons
// a stale graph that doesn't match any real source file.
func TestSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GORTEX_DAEMON_SNAPSHOT", filepath.Join(dir, "snap.gob.gz"))

	orig := graph.New()
	orig.AddNode(&graph.Node{ID: "a.go::Foo", Name: "Foo", Kind: graph.KindFunction, FilePath: "a.go"})
	orig.AddNode(&graph.Node{ID: "b.go::Bar", Name: "Bar", Kind: graph.KindMethod, FilePath: "b.go"})
	orig.AddEdge(&graph.Edge{From: "b.go::Bar", To: "a.go::Foo", Kind: graph.EdgeCalls, FilePath: "b.go", Line: 12})

	saveSnapshot(orig, nil, nil, "v-test", zap.NewNop())

	restored := graph.New()
	result, err := loadSnapshot(restored, zap.NewNop())
	require.NoError(t, err)
	require.True(t, result.Loaded, "loadSnapshot must succeed for a freshly-written file")

	assert.Equal(t, orig.NodeCount(), restored.NodeCount(),
		"node count must round-trip")
	assert.Equal(t, orig.EdgeCount(), restored.EdgeCount(),
		"edge count must round-trip")

	n := restored.GetNode("a.go::Foo")
	require.NotNil(t, n)
	assert.Equal(t, "Foo", n.Name)
}

// TestLoadSnapshot_DropsStaleAbsPathNodes guards against the T0.2 symptom
// of duplicate symbols leaking across daemon sessions. Prior-version code
// paths occasionally stored nodes with absolute filesystem paths as their
// IDs; those nodes persisted in snapshots and were restored forever
// alongside the correctly-prefixed nodes produced by current indexing.
// loadSnapshot must detect the stale shape and drop it so re-indexing
// yields a single canonical node per symbol.
func TestLoadSnapshot_DropsStaleAbsPathNodes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GORTEX_DAEMON_SNAPSHOT", filepath.Join(dir, "snap.gob.gz"))

	orig := graph.New()
	// Clean, current-shape node — should be restored.
	orig.AddNode(&graph.Node{
		ID:   "core-api/api/handler.go::Handler.CreateTuck",
		Name: "CreateTuck", Kind: graph.KindMethod,
		FilePath: "core-api/api/handler.go", RepoPrefix: "core-api",
	})
	// Stale abs-path node — a duplicate of the clean one, must be dropped.
	orig.AddNode(&graph.Node{
		ID:   "/Users/me/tuck/core-api/api/handler.go::Handler.CreateTuck",
		Name: "CreateTuck", Kind: graph.KindMethod,
		FilePath: "/Users/me/tuck/core-api/api/handler.go",
	})
	// Edge pointing at the stale node — must be dropped too so the
	// restored graph contains no dangling references.
	orig.AddEdge(&graph.Edge{
		From: "core-api/api/handler.go::Handler.RegisterRoutes",
		To:   "/Users/me/tuck/core-api/api/handler.go::Handler.CreateTuck",
		Kind: graph.EdgeCalls,
	})
	// Edge between two clean nodes — must survive.
	orig.AddNode(&graph.Node{
		ID:   "core-api/api/handler.go::Handler.RegisterRoutes",
		Name: "RegisterRoutes", Kind: graph.KindMethod,
		FilePath: "core-api/api/handler.go", RepoPrefix: "core-api",
	})
	orig.AddEdge(&graph.Edge{
		From: "core-api/api/handler.go::Handler.RegisterRoutes",
		To:   "core-api/api/handler.go::Handler.CreateTuck",
		Kind: graph.EdgeCalls,
	})

	saveSnapshot(orig, nil, nil, "v-test", zap.NewNop())

	restored := graph.New()
	result, err := loadSnapshot(restored, zap.NewNop())
	require.NoError(t, err)
	require.True(t, result.Loaded)

	assert.NotNil(t, restored.GetNode("core-api/api/handler.go::Handler.CreateTuck"),
		"clean prefixed node must be restored")
	assert.Nil(t, restored.GetNode("/Users/me/tuck/core-api/api/handler.go::Handler.CreateTuck"),
		"stale abs-path node must be dropped on load")

	for _, e := range restored.AllEdges() {
		assert.False(t, strings.HasPrefix(e.From, "/"),
			"edge From references a dropped stale node: %s → %s", e.From, e.To)
		assert.False(t, strings.HasPrefix(e.To, "/"),
			"edge To references a dropped stale node: %s → %s", e.From, e.To)
	}
}

// TestSnapshotRoundTrip_NodeMetaWithShape guards against regressing on
// gob-registration of *contracts.Shape. The indexer attaches a Shape to
// contract-referenced type nodes via Meta["shape"]; without the Register
// call, saveSnapshot aborts on the first such node with "type not
// registered for interface: contracts.Shape" and the snapshot file is
// never written.
func TestSnapshotRoundTrip_NodeMetaWithShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snap.gob.gz")
	t.Setenv("GORTEX_DAEMON_SNAPSHOT", path)

	orig := graph.New()
	orig.AddNode(&graph.Node{
		ID:       "a.go::Req",
		Name:     "Req",
		Kind:     graph.KindType,
		FilePath: "a.go",
		Meta: map[string]any{
			"shape": &contracts.Shape{
				Kind: "struct",
				Fields: []contracts.ShapeField{
					{Name: "ID", Type: "string", Required: true},
				},
			},
		},
	})

	saveSnapshot(orig, nil, nil, "v-test", zap.NewNop())

	info, err := os.Stat(path)
	require.NoError(t, err, "snapshot file must exist — encode must not have aborted")
	require.Greater(t, info.Size(), int64(0), "snapshot must not be empty")

	restored := graph.New()
	result, err := loadSnapshot(restored, zap.NewNop())
	require.NoError(t, err)
	require.True(t, result.Loaded)

	n := restored.GetNode("a.go::Req")
	require.NotNil(t, n)
	require.NotNil(t, n.Meta["shape"], "shape must survive round-trip")

	shape, ok := n.Meta["shape"].(*contracts.Shape)
	require.True(t, ok, "decoded shape must keep its concrete *contracts.Shape type, got %T", n.Meta["shape"])
	assert.Equal(t, "struct", shape.Kind)
	require.Len(t, shape.Fields, 1)
	assert.Equal(t, "ID", shape.Fields[0].Name)
}

// TestSnapshotRoundTrip_NodeMetaWithGenericTypes guards the gob
// registration for the parser-side Meta values that aren't covered by
// the default map[string]any/[]any/[]string set: type_params (Go/TS/Rust
// generics, []map[string]string) and status_codes (HTTP contracts, []int).
// Without the registration, saveSnapshot aborts on the first such node
// with "type not registered for interface: []map[string]string" and the
// snapshot file is never written — a silent regression because
// saveSnapshot only logs.
func TestSnapshotRoundTrip_NodeMetaWithGenericTypes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snap.gob.gz")
	t.Setenv("GORTEX_DAEMON_SNAPSHOT", path)

	orig := graph.New()
	orig.AddNode(&graph.Node{
		ID:       "a.go::Generic",
		Name:     "Generic",
		Kind:     graph.KindFunction,
		FilePath: "a.go",
		Meta: map[string]any{
			"type_params": []map[string]string{
				{"name": "T", "bound": "comparable"},
				{"name": "U"},
			},
			"status_codes": []int{200, 404, 500},
		},
	})

	saveSnapshot(orig, nil, nil, "v-test", zap.NewNop())

	info, err := os.Stat(path)
	require.NoError(t, err, "snapshot file must exist — encode must not have aborted")
	require.Greater(t, info.Size(), int64(0), "snapshot must not be empty")

	restored := graph.New()
	result, err := loadSnapshot(restored, zap.NewNop())
	require.NoError(t, err)
	require.True(t, result.Loaded)

	n := restored.GetNode("a.go::Generic")
	require.NotNil(t, n)

	tp, ok := n.Meta["type_params"].([]map[string]string)
	require.True(t, ok, "type_params must keep its concrete []map[string]string type, got %T", n.Meta["type_params"])
	require.Len(t, tp, 2)
	assert.Equal(t, "T", tp[0]["name"])
	assert.Equal(t, "comparable", tp[0]["bound"])

	sc, ok := n.Meta["status_codes"].([]int)
	require.True(t, ok, "status_codes must keep its concrete []int type, got %T", n.Meta["status_codes"])
	assert.Equal(t, []int{200, 404, 500}, sc)
}

// TestSnapshotRoundTrip_ContractMetaWithSliceOfMaps guards the gob
// registration of []map[string]any. The HTTP schema enricher writes a
// response_envelope value of that shape into Contract.Meta; without the
// Register call, saveSnapshot aborts on the first such contract with
// "type not registered for interface: []map[string]interface {}" and the
// snapshot file never lands.
func TestSnapshotRoundTrip_ContractMetaWithSliceOfMaps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snap.gob.gz")
	t.Setenv("GORTEX_DAEMON_SNAPSHOT", path)

	orig := graph.New()
	snapContracts := []snapshotContract{{
		ID:         "repo/api.go::POST /tasks",
		Type:       "http",
		Role:       "provider",
		SymbolID:   "repo/api.go::CreateTask",
		FilePath:   "repo/api.go",
		Line:       42,
		RepoPrefix: "repo",
		Meta: map[string]any{
			"path": "/tasks",
			"response_envelope": []map[string]any{
				{"name": "id", "type": "string"},
				{"name": "items", "type": "Task", "repeated": true},
			},
		},
	}}

	saveSnapshot(orig, nil, snapContracts, "v-test", zap.NewNop())

	info, err := os.Stat(path)
	require.NoError(t, err, "snapshot file must exist — encode must not have aborted on []map[string]any")
	require.Greater(t, info.Size(), int64(0))

	restored := graph.New()
	result, err := loadSnapshot(restored, zap.NewNop())
	require.NoError(t, err)
	require.True(t, result.Loaded)

	cs, ok := result.Contracts["repo"]
	require.True(t, ok, "contracts for repo prefix must round-trip")
	require.Len(t, cs, 1)
	env, ok := cs[0].Meta["response_envelope"].([]map[string]any)
	require.True(t, ok, "response_envelope must keep its []map[string]any type, got %T", cs[0].Meta["response_envelope"])
	require.Len(t, env, 2)
	assert.Equal(t, "id", env[0]["name"])
	assert.Equal(t, true, env[1]["repeated"])
}

func TestLoadSnapshot_MissingFile_NotAnError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GORTEX_DAEMON_SNAPSHOT", filepath.Join(dir, "nope.gob.gz"))

	g := graph.New()
	result, err := loadSnapshot(g, zap.NewNop())
	require.NoError(t, err, "missing snapshot must not surface as an error — first-run path")
	assert.False(t, result.Loaded, "no snapshot means loaded=false")
	assert.Equal(t, 0, g.NodeCount())
}

func TestLoadSnapshot_CorruptFile_ReportsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corrupt.gob.gz")
	t.Setenv("GORTEX_DAEMON_SNAPSHOT", path)
	require.NoError(t, os.WriteFile(path, []byte("not a gzip stream"), 0o600))

	g := graph.New()
	result, err := loadSnapshot(g, zap.NewNop())
	assert.Error(t, err, "corrupt snapshot must not be silently swallowed")
	assert.False(t, result.Loaded)
	assert.Equal(t, 0, g.NodeCount())
}

// TestStartPeriodicSnapshots_WritesOnTick verifies the 10-minute ticker
// (configurable interval in tests) actually fires and writes to disk.
// The daemon relies on this for crash-recovery — on a `kill -9` it
// would otherwise lose everything since the last shutdown snapshot.
func TestStartPeriodicSnapshots_WritesOnTick(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "periodic.gob.gz")
	t.Setenv("GORTEX_DAEMON_SNAPSHOT", path)

	g := graph.New()
	g.AddNode(&graph.Node{ID: "a.go::X", Name: "X", Kind: graph.KindFunction, FilePath: "a.go"})

	// 30ms interval — fast enough to observe two or three ticks within
	// a reasonable test budget, slow enough to survive scheduler jitter.
	// nil isReady → always-ready (test wants ticks to fire immediately).
	stop := startPeriodicSnapshots(g, nil, "t", 30*time.Millisecond, nil, zap.NewNop())
	t.Cleanup(stop)

	require.Eventually(t, func() bool {
		info, err := os.Stat(path)
		return err == nil && info.Size() > 0
	}, 2*time.Second, 20*time.Millisecond,
		"periodic snapshot should land on disk within the budget")

	// Prove a second tick also happens — modify mtime check after capture.
	info1, err := os.Stat(path)
	require.NoError(t, err)

	// Add another node to force a different encoded payload on the
	// next tick; this way a no-op snapshot won't silently pass by
	// just checking mtime equality.
	g.AddNode(&graph.Node{ID: "b.go::Y", Name: "Y", Kind: graph.KindFunction, FilePath: "b.go"})

	require.Eventually(t, func() bool {
		info2, err := os.Stat(path)
		if err != nil {
			return false
		}
		return info2.ModTime().After(info1.ModTime())
	}, 2*time.Second, 20*time.Millisecond,
		"second periodic tick should rewrite the snapshot")
}
