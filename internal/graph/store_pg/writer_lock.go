package store_pg

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// writerLockKeyBase is the fixed component of the writer advisory-lock key,
// XORed with a hash of the schema identity so that two writers indexing
// different schemas on one PostgreSQL server never false-conflict. The ASCII
// of "gortwrtl". Distinct from the schema-migration advisory key.
const writerLockKeyBase int64 = 0x676F72747772746C

// writerLockAcquireTimeout bounds how long AcquireWriterLock retries before
// declaring a conflict. Kept short: a genuine second writer will not release,
// so waiting longer only delays the clear boot failure.
const writerLockAcquireTimeout = 2 * time.Second

// ErrWriterLockConflict is the sentinel wrapped by WriterLockConflictError.
var ErrWriterLockConflict = errors.New("store_pg: writer lock held by another process")

// WriterLockConflictError is returned by AcquireWriterLock when another
// session already holds the schema's writer lock. HolderPID is the backend
// PID of the current holder when it could be determined from
// pg_stat_activity (0 otherwise).
type WriterLockConflictError struct {
	Schema    string
	HolderPID int
}

func (e *WriterLockConflictError) Error() string {
	holder := "another writer"
	if e.HolderPID != 0 {
		holder = fmt.Sprintf("another writer (backend pid %d)", e.HolderPID)
	}
	sch := e.Schema
	if sch == "" {
		sch = "the default schema"
	}
	return fmt.Sprintf("store_pg: %s already holds the writer lock for %s; "+
		"only one indexing daemon may write a schema at a time (use --follow for read-only replicas)",
		holder, sch)
}

func (e *WriterLockConflictError) Unwrap() error { return ErrWriterLockConflict }

// schemaLockKey derives the advisory-lock key for a schema identity.
func schemaLockKey(schema string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(schema))
	return writerLockKeyBase ^ int64(h.Sum64())
}

// AcquireWriterLock takes a session-scoped PostgreSQL advisory lock keyed on
// the store's schema, over a dedicated pooled connection held for the store's
// lifetime. The lock releases automatically when that connection drops — on
// Close or on process crash — so a crashed writer never strands the lock.
//
// It fails with a *WriterLockConflictError (matchable via
// errors.Is(err, ErrWriterLockConflict)) if the lock cannot be taken within a
// bounded timeout. Read-only stores never call this. Idempotent: a second
// call while the lock is already held is a no-op.
func (s *Store) AcquireWriterLock(ctx context.Context) error {
	if s.readOnly {
		return nil
	}
	if s.lockConn != nil {
		return nil
	}

	// current_schema() reflects the search_path the pool applies, so the key
	// tracks the schema actually written even when Config.Schema is empty.
	var schema string
	if err := s.pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		return fmt.Errorf("store_pg: resolve schema for writer lock: %w", err)
	}
	key := schemaLockKey(schema)

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("store_pg: acquire writer-lock connection: %w", err)
	}

	deadline := time.Now().Add(writerLockAcquireTimeout)
	for {
		var got bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&got); err != nil {
			conn.Release()
			return fmt.Errorf("store_pg: try writer lock: %w", err)
		}
		if got {
			s.lockConn = conn
			s.lockKey = key
			return nil
		}
		if time.Now().After(deadline) {
			pid := s.writerLockHolderPID(ctx, key)
			conn.Release()
			return &WriterLockConflictError{Schema: schema, HolderPID: pid}
		}
		select {
		case <-ctx.Done():
			conn.Release()
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// writerLockHolderPID best-effort resolves the backend PID currently holding
// the advisory lock for key, for a clearer conflict message. Returns 0 when
// it cannot be determined.
func (s *Store) writerLockHolderPID(ctx context.Context, key int64) int {
	// pg_locks splits a bigint advisory key into classid (high 32 bits) and
	// objid (low 32 bits) with objsubid = 1.
	classid := uint32(uint64(key) >> 32)
	objid := uint32(uint64(key) & 0xffffffff)
	var pid int
	err := s.pool.QueryRow(ctx,
		`SELECT pid FROM pg_locks
		 WHERE locktype = 'advisory' AND classid = $1 AND objid = $2 AND objsubid = 1 AND granted
		 LIMIT 1`, classid, objid).Scan(&pid)
	if err != nil {
		return 0
	}
	return pid
}

// releaseWriterLock unlocks and returns the dedicated connection. Called from
// Close. Safe when no lock is held.
func (s *Store) releaseWriterLock() {
	if s.lockConn == nil {
		return
	}
	conn := s.lockConn
	s.lockConn = nil
	// Best-effort explicit unlock; releasing the connection (and thus ending
	// the session) also drops the lock.
	_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, s.lockKey)
	conn.Release()
}

// compile-time check that a pooled connection satisfies the minimal API used.
var _ = (*pgxpool.Conn)(nil)
