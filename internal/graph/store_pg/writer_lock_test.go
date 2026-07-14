package store_pg_test

import (
	"context"
	"errors"
	"testing"

	"github.com/zzet/gortex/internal/graph/store_pg"
)

// A second writer against a schema already locked must be refused.
func TestWriterLock_SecondWriterRefused(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema := createTestSchema(t)
	ctx := context.Background()

	a := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schema})
	if err := a.AcquireWriterLock(ctx); err != nil {
		t.Fatalf("first AcquireWriterLock: %v", err)
	}

	b := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schema})
	err := b.AcquireWriterLock(ctx)
	if !errors.Is(err, store_pg.ErrWriterLockConflict) {
		t.Fatalf("second AcquireWriterLock = %v, want ErrWriterLockConflict", err)
	}
	var conflict *store_pg.WriterLockConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error not a *WriterLockConflictError: %v", err)
	}
}

// Closing the holder releases the lock (drops the session); a fresh writer
// then acquires it — the same release mechanism that fires on crash.
func TestWriterLock_ReleasedOnClose(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema := createTestSchema(t)
	ctx := context.Background()

	a, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schema})
	if err != nil {
		t.Fatalf("Open a: %v", err)
	}
	if err := a.AcquireWriterLock(ctx); err != nil {
		t.Fatalf("acquire a: %v", err)
	}
	if err := a.Close(); err != nil { // releases the lock
		t.Fatalf("close a: %v", err)
	}

	b := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schema})
	if err := b.AcquireWriterLock(ctx); err != nil {
		t.Fatalf("acquire b after a closed = %v, want nil", err)
	}
}

// Two writers whose current_schema differs get distinct lock keys and both
// acquire. (The single global pgvector/pg_trgm extension can only live in one
// schema per database, so the second schema resolves them via a search_path
// that also includes the first — enough to give it a distinct current_schema
// and therefore a distinct lock key.)
func TestWriterLock_DistinctSchemasDoNotContend(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema1 := createTestSchema(t)
	ctx := context.Background()

	schema2 := "wl_distinct_s2"
	_, _ = testRootPool.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema2+` CASCADE`)
	if _, err := testRootPool.Exec(ctx, `CREATE SCHEMA `+schema2); err != nil {
		t.Fatalf("create schema2: %v", err)
	}
	t.Cleanup(func() { _, _ = testRootPool.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema2+` CASCADE`) })

	a := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schema1})
	// schema2 first in search_path (distinct current_schema → distinct key),
	// schema1 second so the extensions and migrated tables resolve.
	b := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schema2 + ", " + schema1})
	if err := a.AcquireWriterLock(ctx); err != nil {
		t.Fatalf("acquire schema1: %v", err)
	}
	if err := b.AcquireWriterLock(ctx); err != nil {
		t.Fatalf("acquire schema2 (should not contend): %v", err)
	}
}

// A follow-mode (read-only) store never takes the writer lock, so a writer
// can still acquire the same schema.
func TestWriterLock_FollowerNeverAcquires(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema := createTestSchema(t)
	ctx := context.Background()

	// Writer migrates the schema, then closes so the lock is free.
	wr, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schema})
	if err != nil {
		t.Fatalf("writer Open: %v", err)
	}
	_ = wr.Close()

	// Read-only open + AcquireWriterLock is a no-op.
	ro := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schema, ReadOnly: true})
	if err := ro.AcquireWriterLock(ctx); err != nil {
		t.Fatalf("follower AcquireWriterLock = %v, want nil no-op", err)
	}

	// A real writer can still take the lock — proof the follower didn't hold it.
	w2 := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schema})
	if err := w2.AcquireWriterLock(ctx); err != nil {
		t.Fatalf("writer AcquireWriterLock after follower = %v, want nil", err)
	}
}
