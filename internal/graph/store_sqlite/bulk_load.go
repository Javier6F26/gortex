package store_sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/zzet/gortex/internal/graph"
)

// Compile-time assertion: *Store satisfies graph.BulkLoader.
var _ graph.BulkLoader = (*Store)(nil)

// bulkDroppableIndex is one secondary index the bulk-load fast path drops
// before a first/empty cold index and rebuilds afterward.
type bulkDroppableIndex struct {
	name string
	ddl  string
}

// bulkDroppableIndexes is the single source of truth for these index
// definitions. Open creates them (so the initial DB has them), BeginBulkLoad
// drops them by name, and FlushBulk recreates them from the exact same ddl —
// keeping the initial and post-bulk shapes from drifting.
//
// These are exactly the standalone, NON-UNIQUE CREATE INDEX statements over
// the large nodes / edges tables. Maintaining them per-row across a
// multi-hundred-thousand-row cold load is pure overhead when the rows land
// once, so they are dropped up front and rebuilt in one pass at the end.
//
// Deliberately excluded:
//   - nodes_by_qual (UNIQUE): enforces qual_name dedup on every
//     INSERT OR REPLACE. Dropping it would change insert conflict semantics
//     (collapsed qual_name collisions would diverge from the non-bulk path)
//     and a duplicate could make the recreate fail. It stays live.
//   - the edges UNIQUE(from_id, …) table constraint and every WITHOUT ROWID
//     primary-key index: not standalone indexes; they cannot be dropped while
//     the table/constraint exists.
//   - edges_external (partial): a tiny index over external-call terminals,
//     created from a shared predicate in Open; not worth dropping.
//
// Dropping/recreating these is a runtime operation on identical DDL — it is
// NOT a schema change, so it does not touch the persisted schema version.
var bulkDroppableIndexes = []bulkDroppableIndex{
	{"nodes_by_name", `CREATE INDEX IF NOT EXISTS nodes_by_name ON nodes(name)`},
	{"nodes_by_kind", `CREATE INDEX IF NOT EXISTS nodes_by_kind ON nodes(kind)`},
	{"nodes_by_file", `CREATE INDEX IF NOT EXISTS nodes_by_file ON nodes(file_path)`},
	{"nodes_by_repo", `CREATE INDEX IF NOT EXISTS nodes_by_repo ON nodes(repo_prefix) WHERE repo_prefix <> ''`},
	{"edges_by_from", `CREATE INDEX IF NOT EXISTS edges_by_from ON edges(from_id, kind)`},
	{"edges_by_to", `CREATE INDEX IF NOT EXISTS edges_by_to ON edges(to_id, kind)`},
	{"edges_by_kind", `CREATE INDEX IF NOT EXISTS edges_by_kind ON edges(kind)`},
	// Partial index over exactly the not-yet-semantically-stamped nodes per
	// repo. Stays small in steady state (most nodes end up stamped), so a
	// future "unstamped nodes in this repo" query is an index scan over the
	// residual few instead of a full-table decode of every node's meta.
	{"nodes_semantic_pending", `CREATE INDEX IF NOT EXISTS nodes_semantic_pending ON nodes(repo_prefix) WHERE semantic_type IS NULL`},
}

// bulkCacheSizeKiB is the page cache the fast path requests on its pinned
// connection. SQLite reads a negative cache_size as a KiB budget, so this is
// ~256 MiB — large enough to keep the cold load's working set resident.
const bulkCacheSizeKiB = -262144

// beginWrite starts a write transaction. During a bulk-load fast path it pins
// the single connection that carries synchronous=OFF + the enlarged page
// cache (database/sql PRAGMAs are connection-local, so a pooled connection
// would not see them); otherwise it uses the shared pool. The caller holds
// writeMu, which also guards s.bulkConn.
func (s *Store) beginWrite() (*sql.Tx, error) {
	if s.bulkConn != nil {
		return s.bulkConn.BeginTx(context.Background(), nil)
	}
	return s.db.Begin()
}

// BeginBulkLoad enters the bulk-load fast path for a first/empty cold index.
// It pins one connection at synchronous=OFF with an enlarged page cache and
// drops the droppable secondary indexes, so a multi-hundred-thousand-row load
// skips per-row B-tree maintenance and per-commit fsync. FlushBulk reverses
// all of it: restore the pragmas, rebuild the indexes, and checkpoint.
//
// Gated: it engages ONLY when the nodes table is empty. On a populated store
// (incremental reindex, warm restart, or a later repo in a multi-repo cold
// start that shares the disk store) it is a safe no-op — dropping indexes or
// disabling crash durability under live, concurrently-readable rows would be
// unsafe. In-memory stores have no WAL / on-disk B-tree pressure, so it is a
// no-op there too.
func (s *Store) BeginBulkLoad() {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	// Re-entrancy / non-disk guard: a second BeginBulkLoad without an
	// intervening FlushBulk, or an in-memory store, stays a no-op.
	if s.bulkConn != nil || isMemoryPath(s.dbPath) {
		return
	}

	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return
	}

	// Gate to a genuinely first/empty index.
	if !nodesTableEmpty(ctx, conn) {
		_ = conn.Close()
		return
	}

	// Capture prior pragma values so FlushBulk (and every early-return /
	// error path) can restore them. If they can't be read, don't engage —
	// a slow correct load beats a connection stuck at synchronous=OFF.
	prevSync, err := pragmaInt(ctx, conn, "synchronous")
	if err != nil {
		_ = conn.Close()
		return
	}
	prevCache, err := pragmaInt(ctx, conn, "cache_size")
	if err != nil {
		_ = conn.Close()
		return
	}

	// synchronous=OFF drops crash durability for the load window —
	// acceptable only because a crash on a fresh index just re-indexes.
	if _, err := conn.ExecContext(ctx, "PRAGMA synchronous = OFF"); err != nil {
		_ = conn.Close()
		return
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA cache_size = %d", bulkCacheSizeKiB)); err != nil {
		// Roll the durability change back before bailing.
		_, _ = conn.ExecContext(ctx, fmt.Sprintf("PRAGMA synchronous = %d", prevSync))
		_ = conn.Close()
		return
	}

	// Drop the droppable secondary indexes; rebuilt in one pass at
	// FlushBulk. Best-effort: a failed drop just means that index keeps
	// being maintained per-row (slower, still correct), so it is not fatal.
	for _, idx := range bulkDroppableIndexes {
		_, _ = conn.ExecContext(ctx, "DROP INDEX IF EXISTS "+idx.name)
	}

	s.bulkConn = conn
	s.bulkPrevSync = prevSync
	s.bulkPrevCacheSize = prevCache
}

// FlushBulk exits the bulk-load fast path: it rebuilds every index
// BeginBulkLoad dropped, restores synchronous + cache_size on the pinned
// connection, runs one TRUNCATE checkpoint to drain the WAL the no-fsync load
// grew, and returns the connection to the pool. It is a no-op when no fast
// path is active (BeginBulkLoad gated out, or already flushed).
//
// The pragma restore + connection release run unconditionally (defer), so a
// failure mid-rebuild can never leave a connection stuck at synchronous=OFF
// in the pool.
func (s *Store) FlushBulk() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	conn := s.bulkConn
	if conn == nil {
		return nil
	}
	// Detach first: the fast path is over regardless of the outcome below.
	s.bulkConn = nil

	ctx := context.Background()
	defer func() {
		// Always restore durability + cache and release the connection,
		// even if an index rebuild failed.
		_, _ = conn.ExecContext(ctx, fmt.Sprintf("PRAGMA synchronous = %d", s.bulkPrevSync))
		_, _ = conn.ExecContext(ctx, fmt.Sprintf("PRAGMA cache_size = %d", s.bulkPrevCacheSize))
		_ = conn.Close()
	}()

	for _, idx := range bulkDroppableIndexes {
		if _, err := conn.ExecContext(ctx, idx.ddl); err != nil {
			return fmt.Errorf("store_sqlite: rebuild index %s: %w", idx.name, err)
		}
	}

	// Drain the WAL the no-fsync bulk window grew back into the main DB and
	// truncate the -wal file. Same TRUNCATE mode as runCheckpointLoop, so it
	// cooperates with the journal_size_limit / periodic-checkpoint policy.
	if _, err := conn.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("store_sqlite: bulk checkpoint: %w", err)
	}
	return nil
}

// nodesTableEmpty reports whether the nodes table holds no rows. Used to gate
// the bulk-load fast path to a genuinely first/empty cold index.
func nodesTableEmpty(ctx context.Context, conn *sql.Conn) bool {
	var one int
	err := conn.QueryRowContext(ctx, "SELECT 1 FROM nodes LIMIT 1").Scan(&one)
	return errors.Is(err, sql.ErrNoRows)
}

// pragmaInt reads a single-integer PRAGMA (synchronous, cache_size) off the
// given connection.
func pragmaInt(ctx context.Context, conn *sql.Conn, pragma string) (int64, error) {
	var v int64
	if err := conn.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}
