package indexer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/graph/store_pg"
	"github.com/zzet/gortex/internal/parser"
	"github.com/zzet/gortex/internal/parser/languages"
)

// openIndexerPGStore opens a fresh, isolated PostgreSQL schema for an indexer
// test, or skips when no test PG is configured. PG is the only backend that is
// both a BulkLoader (so the shadow swap engages) and a FileBlobWriter (so it
// can persist blobs) — sqlite is a BulkLoader but stores no blobs.
func openIndexerPGStore(t *testing.T) *store_pg.Store {
	t.Helper()
	dsn := os.Getenv("GORTEX_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("no postgres connection; set GORTEX_TEST_PG_DSN to enable")
	}
	ctx := context.Background()
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect to test PG: %v", err)
	}
	if err := root.Ping(ctx); err != nil {
		root.Close()
		t.Skipf("test PG not reachable: %v", err)
	}
	schema := fmt.Sprintf("idx_blob_%d", os.Getpid())
	_, _ = root.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	if _, err := root.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		root.Close()
		t.Fatalf("create schema: %v", err)
	}
	// Extensions are database-global; recreate them in the test schema so its
	// search_path resolves the pg_trgm / vector types (mirrors createTestSchema).
	_, _ = root.Exec(ctx, `DROP EXTENSION IF EXISTS pg_trgm CASCADE`)
	_, _ = root.Exec(ctx, `DROP EXTENSION IF EXISTS vector CASCADE`)
	if _, err := root.Exec(ctx, `CREATE EXTENSION pg_trgm WITH SCHEMA `+schema); err != nil {
		root.Close()
		t.Fatalf("create pg_trgm: %v", err)
	}
	if _, err := root.Exec(ctx, `CREATE EXTENSION vector WITH SCHEMA `+schema); err != nil {
		root.Close()
		t.Fatalf("create vector: %v", err)
	}
	t.Cleanup(func() {
		_, _ = root.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		root.Close()
	})
	store, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schema})
	if err != nil {
		t.Fatalf("open store_pg: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestBulkLoad_PersistsFileBlobsToBackend guards the fix where a cold index run
// under the bulk-load shadow swap dropped the file_blobs (and files) rows. The
// first index of a repo redirects idx.graph at the in-memory shadow and drains
// only nodes/edges to disk; persistFileMeta wrote blobs to the shadow, so they
// never reached the backend. A diskless follower then had an empty file_blobs
// table and returned follow_no_disk for every code read.
//
// The fix captures the disk store at the shadow swap (idx.fileMetaSink /
// idx.blobSink) — mirroring the vector-persist fix — so blobs land on the
// backend. This test cold-indexes a tiny repo into a PostgreSQL store (the only
// backend that is both a BulkLoader and a FileBlobWriter) and asserts the blob
// is retrievable byte-exact; it was absent before the fix.
func TestBulkLoad_PersistsFileBlobsToBackend(t *testing.T) {
	store := openIndexerPGStore(t)

	// Sanity: PG is a BulkLoader (so the shadow swap engages, the path that
	// regressed) and a FileBlobWriter (so it CAN persist blobs).
	_, isBulk := graph.Store(store).(graph.BulkLoader)
	require.True(t, isBulk, "store must be a BulkLoader to exercise the shadow swap")
	_, isBlob := graph.Store(store).(graph.FileBlobWriter)
	require.True(t, isBlob, "store must be a FileBlobWriter to persist blobs")

	dir := t.TempDir()
	const body = "package calc\n\nfunc Add(x, y int) int {\n\treturn x + y\n}\n"
	writeFile(t, filepath.Join(dir, "calc.go"), body)

	reg := parser.NewRegistry()
	reg.Register(languages.NewGoExtractor())
	cfg := config.Default().Index
	cfg.Workers = 2
	idx := New(store, reg, cfg, zap.NewNop())

	_, err := idx.IndexCtx(context.Background(), dir)
	require.NoError(t, err)

	// The blob for the indexed file must be on the backend, byte-exact.
	refs, err := store.IndexedFileBlobs()
	require.NoError(t, err)
	require.NotEmpty(t, refs,
		"cold index must populate file_blobs on the backend under the bulk loader "+
			"(regression: blobs lived only in the drained in-memory shadow and never reached disk)")

	var hash string
	for _, r := range refs {
		if filepath.Base(r.FilePath) == "calc.go" {
			hash = r.ContentHash
		}
	}
	require.NotEmpty(t, hash, "calc.go must have a stored blob")
	blob, ok := store.GetFileBlobByHash(hash)
	require.True(t, ok, "blob must be retrievable by hash")
	require.Equal(t, body, string(blob.Body), "stored blob must be byte-exact")
}
