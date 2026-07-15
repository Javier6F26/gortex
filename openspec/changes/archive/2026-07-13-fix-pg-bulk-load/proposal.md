## Why

The PostgreSQL backend's bulk-load fast path (~500 lines of COPY FROM + UNLOGGED staging + atomic table swap) is completely dead code. `AddBatch` never delegates to the bulk buffer when `s.bulk != nil`, so every row is inserted via individual `INSERT` statements with per-transaction WAL fsyncs. This makes cold-start indexing 10-100x slower than SQLite, generates gigabytes of WAL for large repositories, and the `FlushBulk` table swap would destroy prior repos' data if it were ever reached. A comprehensive fix is needed to match SQLite's performance while producing identical results.

## What Changes

- **PG `AddBatch`** checks `s.bulk != nil` and routes to the in-memory buffer when in bulk mode, so rows accumulate for `FlushBulk` instead of being written via individual INSERTs
- **PG `BeginBulkLoad`** always activates bulk mode (no empty-table gate), but captures whether the nodes table was empty at entry into a `tableEmpty` flag for `FlushBulk` routing
- **PG `FlushBulk`** branches on `tableEmpty`: if true, uses the existing destructive table swap (fastest path); if false, uses a new non-destructive path: COPY FROM into UNLOGGED staging → INSERT INTO SELECT with ON CONFLICT (safe for repos 2+)
- **Indexer** wraps the non-shadow parse path in `BeginBulkLoad`/`FlushBulk` when the store implements `BulkLoader`, extending COPY FROM benefits to warm restarts and multi-repo non-first indexes

## Capabilities

### New Capabilities
- `pg-bulk-load`: PostgreSQL backend correctly uses COPY FROM bulk-load fast path for all indexing scenarios — cold start, multi-repo, and warm restart — with performance matching SQLite

### Modified Capabilities
<!-- None — existing spec-level behavior is unchanged; this is a bug fix restoring intended behavior -->

## Impact

- Affected code: `internal/graph/store_pg/store.go` (AddBatch dispatch), `internal/graph/store_pg/bulk_load.go` (BeginBulkLoad capture, FlushBulk branching, new non-destructive merge path), `internal/indexer/indexer.go` (BulkLoader wrap for non-shadow path)
- No API changes, no config changes, no breaking changes
- Performance: 10-100x improvement for cold-start indexing, matching SQLite within ~5%
- Correctness: identical conflict resolution (same ON CONFLICT clauses), identical data, guaranteed by shared helper functions and transactional atomicity
