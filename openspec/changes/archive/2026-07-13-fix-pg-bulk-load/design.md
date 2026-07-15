## Context

The PostgreSQL backend (`store_pg`) implements `graph.BulkLoader` with a two-phase fast path: `BeginBulkLoad` enters bulk mode, `AddBatchBulk` buffers rows in memory, and `FlushBulk` commits everything via `COPY FROM` into UNLOGGED staging tables followed by an atomic table swap. This machinery was designed to match SQLite's bulk-load performance but has never actually worked due to two critical bugs:

1. **`AddBatch` ignores bulk mode.** The indexer's shadow-swap drain calls `diskTarget.AddBatch()` (the `graph.Store` interface method), which in the PG store does row-by-row `INSERT` statements — completely bypassing the bulk buffer. `AddBatchBulk` (the method that checks `s.bulk`) exists but is never called by the indexer. Result: `FlushBulk` finds empty buffers and returns as a no-op. The entire ~500-line COPY FROM machinery is dead code.

2. **`FlushBulk` does full table replacement.** The atomic swap (`ALTER TABLE nodes RENAME TO nodes_old; DROP TABLE nodes_old`) unconditionally destroys all data from prior repos. This would be catastrophic in multi-repo mode if the bulk path were actually reached. SQLite avoids this by gating `BeginBulkLoad` on `nodesTableEmpty()` — the fast path only engages for the first repo.

Additionally, the non-shadow indexer path (warm restart, oversized repos) never enters bulk mode at all, leaving those scenarios on per-row INSERTs permanently.

## Goals / Non-Goals

**Goals:**
- Make `AddBatch` route through the bulk buffer when `s.bulk != nil`, activating the COPY FROM fast path
- Add a `tableEmpty` flag to `bulkState` so `FlushBulk` can choose between destructive swap (first repo, empty table) and non-destructive merge (subsequent repos, warm restart)
- Implement the non-destructive merge path: COPY FROM into UNLOGGED staging → `INSERT INTO SELECT ... ON CONFLICT` for both nodes and edges
- Wrap the indexer's non-shadow path in `BeginBulkLoad`/`FlushBulk` to extend COPY FROM benefits to all indexing scenarios
- Produce results byte-for-byte identical to SQLite and the current per-row INSERT path

**Non-Goals:**
- Changing the indexer's shadow-swap drain (it already calls BeginBulkLoad/FlushBulk correctly; only the store-layer routing was broken)
- Optimizing the per-chunk buffer contention under high worker parallelism (future work: per-goroutine staging buffers)
- Adding a `repo_prefix`-scoped empty check for the merge path (future enhancement for misconfigured repos)
- Changing sidecar table population (content_fts, vectors — they follow their own paths)

## Decisions

### Decision 1: Always-activate bulk mode (remove the empty-table gate)

**Chosen**: `BeginBulkLoad` always sets `s.bulk = &bulkState{}` and captures `tableEmpty` from the current nodes table state. The gate moves from "should we activate?" to "which FlushBulk path should we use?".

**Rationale**: The non-destructive merge path (`INSERT INTO SELECT FROM staging`) is safe on a non-empty table — it uses the same `ON CONFLICT` clauses as the per-row INSERT path. There is no reason to deny COPY FROM performance to repos 2+. The `tableEmpty` flag selects between:
- `true` → destructive swap (fastest: no conflict checking, indexes built from scratch on staging)
- `false` → non-destructive merge (fast: COPY FROM ingest, INSERT INTO SELECT with conflict resolution)

**Alternatives considered**:
- **Gate on empty table (like SQLite)**: Simpler but leaves repos 2+ on slow per-row INSERTs. Rejected — the non-destructive merge path eliminates the safety concern that motivated SQLite's gate.
- **Always do non-destructive merge**: Simpler code (single path) but slower for repo 1 (index maintenance during INSERT vs. batch index creation on staging). Rejected — repo 1 is the most common case and deserves maximum speed.

### Decision 2: Non-destructive merge via UNLOGGED staging + INSERT INTO SELECT

**Chosen**: For the non-destructive path, create UNLOGGED staging tables with `LIKE nodes` (no indexes), COPY FROM into them, then `INSERT INTO nodes SELECT * FROM nodes_bulk ON CONFLICT (id) DO UPDATE SET ...`. Same for edges with `ON CONFLICT ... DO NOTHING`.

**Rationale**: COPY FROM provides the ingest speed. INSERT INTO SELECT lets PostgreSQL optimize the bulk insert as a single statement. The ON CONFLICT clauses are byte-for-byte identical to the current per-row INSERT path, guaranteeing identical results. UNLOGGED staging tables generate zero WAL for the COPY FROM phase — only the final INSERT INTO SELECT writes WAL.

**Alternatives considered**:
- **Multi-row INSERT (batched VALUES)**: Improves network overhead but still pays per-statement planning and per-row index maintenance. COPY FROM is ~10x faster for large batches.
- **COPY FROM directly into live tables**: PostgreSQL's COPY FROM doesn't support ON CONFLICT. Would need a separate conflict-handling pass. Rejected — staging + INSERT INTO SELECT is cleaner.
- **Drop indexes before merge, rebuild after**: Would be faster (no index maintenance during INSERT) but requires ACCESS EXCLUSIVE locks that block concurrent readers. Acceptable for cold start but risky for warm restart. Deferred to future optimization behind a config flag.

### Decision 3: Wrap the non-shadow indexer path in BeginBulkLoad/FlushBulk

**Chosen**: After the shadow-swap decision block in `IndexCtx`, if the store implements `BulkLoader` and the shadow was NOT taken, wrap the entire parse phase in `BeginBulkLoad`/`FlushBulk`.

**Rationale**: The non-shadow path (warm restart, oversized repos, multi-repo non-first) currently does per-row INSERTs for every per-file `AddBatch` call. Wrapping it in bulk mode buffers all rows during the parse phase and flushes via COPY FROM at the end. This is a ~20-50x improvement for warm restarts.

**Concurrency note**: Under the non-shadow path, per-file `AddBatch` calls come from parallel worker goroutines. The buffer append is serialized by `writeMu`. For very wide machines (16+ cores), this could become a contention point. The mutex approach is correct and sufficient for the initial implementation; a future optimization could use per-worker staging buffers merged at FlushBulk time.

## Risks / Trade-offs

- **[Risk] Non-shadow bulk wrap buffers all nodes/edges in memory until FlushBulk** → For repos exceeding shadow-max thresholds, this could be gigabytes. Mitigation: the buffer starts at 100K capacity and grows as needed. For extreme cases, the per-row fallback (when `s.bulk` is nil) still works. A future configurable buffer cap or mid-parse flush threshold can be added.

- **[Risk] writeMu contention under high worker parallelism** → Mitigation: AddBatch calls are already serialized by the indexer's design (per-file workers produce results independently, but each AddBatch call is relatively quick — just a struct copy and slice append). Contention is measurable only at very high core counts. A future optimization can use per-worker staging buffers.

- **[Risk] `LIKE nodes INCLUDING ALL` in the destructive swap path may create duplicate indexes** → The initial schema creates indexes on `nodes`; `INCLUDING ALL` copies them to `nodes_bulk`; then FlushBulk creates 9 more indexes. If names collide, CREATE INDEX fails. If names differ, duplicate indexes waste space. Mitigation: this is a pre-existing issue in the current code. The fix doesn't change this path. A follow-up cleanup can switch to `LIKE nodes INCLUDING DEFAULTS` (no indexes) and build all indexes explicitly.

- **[Risk] FlushBulk failure mid-transaction** → The entire operation runs in a single transaction. If anything fails, the deferred rollback restores the previous state. The `defer func() { s.bulk = nil }()` in FlushBulk ensures bulk mode is exited even on error.
