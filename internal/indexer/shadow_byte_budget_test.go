package indexer

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/graph/store_sqlite"
	"github.com/zzet/gortex/internal/parser"
	"github.com/zzet/gortex/internal/parser/languages"
)

func TestShadowMaxBytes(t *testing.T) {
	require.Equal(t, defaultShadowMaxBytes, shadowMaxBytes(), "unset falls back to the default")
	t.Setenv("GORTEX_SHADOW_MAX_BYTES", "4096")
	require.Equal(t, int64(4096), shadowMaxBytes(), "a positive override wins")
	t.Setenv("GORTEX_SHADOW_MAX_BYTES", "0")
	require.Equal(t, defaultShadowMaxBytes, shadowMaxBytes(), "zero falls back to the default")
	t.Setenv("GORTEX_SHADOW_MAX_BYTES", "-5")
	require.Equal(t, defaultShadowMaxBytes, shadowMaxBytes(), "a negative value falls back to the default")
	t.Setenv("GORTEX_SHADOW_MAX_BYTES", "notanumber")
	require.Equal(t, defaultShadowMaxBytes, shadowMaxBytes(), "a non-numeric value falls back to the default")
}

// TestShadowSwap_ByteBudgetRoutesToDiskPath verifies the byte gate added
// for #120: a repo whose combined input bytes exceed the budget but whose
// file count is far under shadowMaxFileCount — the few-huge-files content
// shape — must be refused the all-in-memory shadow and built against the
// bounded per-call disk path instead. Crucially the gate changes only the
// build strategy, never the result: the same node set must come out either
// way.
func TestShadowSwap_ByteBudgetRoutesToDiskPath(t *testing.T) {
	dir := t.TempDir()
	// 8 files, ~50 KiB each (~400 KiB total). Far under the 50k file-count
	// ceiling, so only the byte gate can trip.
	const perFile = 50 * 1024
	for i := 0; i < 8; i++ {
		writeFile(t, filepath.Join(dir, "f"+strconv.Itoa(i)+".go"),
			"package p\n\nvar Blob"+strconv.Itoa(i)+" = \""+strings.Repeat("x", perFile)+"\"\n")
	}

	run := func(t *testing.T, budget string) (decision map[string]any, nodes map[string]bool) {
		t.Helper()
		t.Setenv("GORTEX_SHADOW_MAX_BYTES", budget)
		store, err := store_sqlite.Open(filepath.Join(t.TempDir(), "store.sqlite"))
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		// Sanity: only sqlite's BulkLoader path exercises the shadow swap.
		_, isBulk := graph.Store(store).(graph.BulkLoader)
		require.True(t, isBulk, "sqlite must implement BulkLoader for this test")

		core, logs := observer.New(zapcore.InfoLevel)
		reg := parser.NewRegistry()
		languages.RegisterAll(reg)
		cfg := config.Default().Index
		cfg.Workers = 2
		_, err = New(store, reg, cfg, zap.New(core)).IndexCtx(context.Background(), dir)
		require.NoError(t, err)

		entries := logs.FilterMessage("indexer: shadow-swap decision").All()
		require.NotEmpty(t, entries, "the shadow-swap decision must be logged")
		decision = entries[0].ContextMap()

		nodes = map[string]bool{}
		for _, n := range store.AllNodes() {
			nodes[n.ID] = true
		}
		return decision, nodes
	}

	// Tiny budget (64 KiB): combined input (~400 KiB) exceeds it while the
	// file count stays under the ceiling → the shadow must be refused.
	over, overNodes := run(t, "65536")
	require.True(t, over["below_shadow_max"].(bool),
		"file count is under the count ceiling — only the byte gate should trip")
	require.False(t, over["below_shadow_bytes"].(bool),
		"combined input exceeds the byte budget")
	require.False(t, over["shadow_taken"].(bool),
		"over the byte budget the in-memory shadow must be refused")

	// Generous budget (1 GiB): the same files now fit → the shadow engages.
	under, underNodes := run(t, "1073741824")
	require.True(t, under["below_shadow_bytes"].(bool),
		"combined input fits the generous byte budget")
	require.True(t, under["shadow_taken"].(bool),
		"under the byte budget the shadow path engages")

	// The byte gate is a build-strategy switch, not a content filter: the
	// bounded disk path and the shadow path must produce identical graphs.
	require.Equal(t, underNodes, overNodes,
		"byte gate must not drop, add, or duplicate any node")
}
