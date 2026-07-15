## 1. Store layer: AddBatch bulk dispatch

- [x] 1.1 Add bulk-mode guard at top of `Store.AddBatch` in `internal/graph/store_pg/store.go`: if `s.bulk != nil`, call `s.bufferBatchLocked(nodes, edges)` and return
- [x] 1.2 Extract existing `AddBatchBulk` buffering logic into a shared `bufferBatchLocked` helper that both `AddBatch` (in bulk mode) and `AddBatchBulk` call
- [x] 1.3 Ensure `bufferBatchLocked` skips nil/empty-ID/proxy nodes and edges (matching current `AddBatchBulk` behavior)

## 2. Store layer: BeginBulkLoad always-activate

- [x] 2.1 Add `tableEmpty bool` field to `bulkState` struct in `internal/graph/store_pg/bulk_load.go`
- [x] 2.2 Modify `BeginBulkLoad` to always activate bulk mode (remove empty-table gate), but capture `tableEmpty` via `SELECT NOT EXISTS (SELECT 1 FROM nodes LIMIT 1)`
- [x] 2.3 On query error, default to `tableEmpty: false` and log a warning (safe fallback to non-destructive path)

## 3. Store layer: FlushBulk non-destructive merge path

- [x] 3.1 Add `else` branch in `FlushBulk` for `!s.bulk.tableEmpty`: create UNLOGGED staging tables with `LIKE {nodes,edges}` (no `INCLUDING ALL`, no indexes needed)
- [x] 3.2 Implement COPY FROM into staging tables (reuse existing `pgx.CopyFrom` logic)
- [x] 3.3 Implement `INSERT INTO nodes SELECT * FROM nodes_bulk ON CONFLICT (id) DO UPDATE SET ...` using the full `nodeInsertConflict` clause
- [x] 3.4 Implement `INSERT INTO edges SELECT * FROM edges_bulk ON CONFLICT (from_id, to_id, kind, file_path, line) DO NOTHING`
- [x] 3.5 Drop staging tables after merge
- [x] 3.6 Ensure `SET LOCAL synchronous_commit TO OFF` and `SET LOCAL maintenance_work_mem TO '1GB'` apply to both paths

## 4. Indexer: Bulk-wrap non-shadow path

- [x] 4.1 In `internal/indexer/indexer.go` `IndexCtx`, after the shadow-swap decision block, add: if `bl, ok := idx.graph.(graph.BulkLoader); ok && !shadowTaken`, call `bl.BeginBulkLoad()` before the worker pool
- [x] 4.2 Add deferred `bl.FlushBulk()` call, gated on `retErr == nil`, matching the existing shadow-swap drain pattern
- [x] 4.3 Ensure `writeMu` serialization is safe for concurrent per-file `AddBatch` calls from parallel workers

## 5. Verify

- [x] 5.1 Run existing PG store tests: `go test -race ./internal/graph/store_pg/...`
- [x] 5.2 Run full test suite: `go test -race ./...`
- [ ] 5.3 Manual verification: clean PG database, run `gortex daemon start --backend postgres --pg-dsn <dsn>`, confirm all repos index, freshness reaches complete, and total time is comparable to SQLite
- [ ] 5.4 Verify multi-repo correctness: check that node/edge counts match between PG and SQLite backends for the same project
- [ ] 5.5 Verify warm restart: kill daemon mid-index, restart, confirm incremental reindex completes without errors
