# pg-bulk-load Specification

## Purpose
Bulk-load semantics for the PostgreSQL graph store: COPY-FROM buffering, destructive vs. non-destructive flush paths, crash-safe/replica-visible live tables, per-process-unique staging, and stale-row cleanup on re-index. Established by `fix-pg-bulk-load` and hardened by `harden-pg-store`.

## Requirements
### Requirement: Bulk-mode AddBatch dispatch

The PostgreSQL store's `AddBatch` method SHALL check `s.bulk != nil` and delegate to the in-memory buffer when a bulk-load session is active, so that rows accumulate for `FlushBulk` via COPY FROM instead of being written individually.

#### Scenario: AddBatch buffers in bulk mode
- **WHEN** `BeginBulkLoad` has been called and `s.bulk != nil`
- **AND** `AddBatch` is called with nodes and edges
- **THEN** rows SHALL be buffered in `s.bulk.nodes` and `s.bulk.edges` as `pgNodeRow` and `pgEdgeRow` structs
- **AND** no database writes SHALL occur

#### Scenario: AddBatch inserts directly outside bulk mode
- **WHEN** no bulk-load session is active (`s.bulk == nil`)
- **AND** `AddBatch` is called with nodes and edges
- **THEN** rows SHALL be inserted directly into the `nodes` and `edges` tables via individual INSERT statements in a transaction
- **AND** the `nodeInsertConflict` upsert and edge `ON CONFLICT DO NOTHING` semantics SHALL be preserved

### Requirement: BeginBulkLoad always-activates with table-empty capture

The PostgreSQL store's `BeginBulkLoad` method SHALL always activate bulk mode by setting `s.bulk` to a new `bulkState`, and SHALL capture whether the `nodes` table was empty at entry into a `tableEmpty` flag for `FlushBulk` routing.

#### Scenario: BeginBulkLoad activates on empty store
- **WHEN** the `nodes` table contains zero rows
- **AND** `BeginBulkLoad` is called
- **THEN** `s.bulk` SHALL be set to a new `bulkState` with `tableEmpty: true`

#### Scenario: BeginBulkLoad activates on non-empty store
- **WHEN** the `nodes` table contains one or more rows
- **AND** `BeginBulkLoad` is called
- **THEN** `s.bulk` SHALL be set to a new `bulkState` with `tableEmpty: false`

#### Scenario: BeginBulkLoad is re-entrant safe
- **WHEN** a bulk-load session is already active (`s.bulk != nil`)
- **AND** `BeginBulkLoad` is called again
- **THEN** the call SHALL log a warning and return without modifying `s.bulk`

### Requirement: FlushBulk destructive swap path for empty table

When `tableEmpty` is true, `FlushBulk` SHALL use the destructive table-swap path: create UNLOGGED staging tables with per-process unique names, COPY FROM into them, build indexes, convert the staging tables to LOGGED, and atomically swap staging tables with live tables. The live `nodes`/`edges` tables resulting from the swap MUST be LOGGED (crash-safe and visible to physical read replicas).

#### Scenario: Destructive swap on first repo
- **WHEN** a bulk-load session is active with `tableEmpty: true` and buffered rows
- **AND** `FlushBulk` is called
- **THEN** UNLOGGED staging tables SHALL be created with `LIKE {nodes,edges} INCLUDING ALL` under per-process unique names (`nodes_bulk_<pid>_<nonce>`, `edges_bulk_<pid>_<nonce>`)
- **AND** buffered rows SHALL be written to staging tables via COPY FROM
- **AND** indexes SHALL be built on staging tables
- **AND** staging tables SHALL be converted with `ALTER TABLE ... SET LOGGED` before the rename
- **AND** live tables SHALL be atomically replaced via ALTER TABLE RENAME
- **AND** old tables SHALL be dropped
- **AND** the transaction SHALL be committed
- **AND** `s.bulk` SHALL be set to nil

#### Scenario: Live tables survive crash recovery and replicate
- **WHEN** the destructive swap has committed
- **THEN** `pg_class.relpersistence` for `nodes` and `edges` SHALL be `p` (permanent/LOGGED)
- **AND** a physical read replica SHALL serve the swapped rows

### Requirement: FlushBulk non-destructive merge path for non-empty table

When `tableEmpty` is false, `FlushBulk` SHALL use the non-destructive merge path: create UNLOGGED staging tables with per-process unique names and no indexes, COPY FROM into them, INSERT INTO SELECT into the live tables with ON CONFLICT handling, and — within the same transaction — delete rows belonging to the flushed repo prefix that are absent from staging, so a re-index fully replaces the repo's rows.

#### Scenario: Non-destructive merge on subsequent repos
- **WHEN** a bulk-load session is active with `tableEmpty: false` and buffered rows
- **AND** `FlushBulk` is called
- **THEN** UNLOGGED staging tables SHALL be created with `LIKE {nodes,edges}` (no INCLUDING clause, no indexes) under per-process unique names
- **AND** buffered rows SHALL be written to staging tables via COPY FROM
- **AND** nodes SHALL be merged into the live `nodes` table via `INSERT INTO nodes SELECT ... ON CONFLICT (id) DO UPDATE SET` with the full `nodeInsertConflict` clause
- **AND** edges SHALL be merged into the live `edges` table via `INSERT INTO edges SELECT ... ON CONFLICT (from_id, to_id, kind, file_path, line) DO NOTHING`
- **AND** rows in `nodes` with the flushed repo prefix whose `id` is absent from the staging table SHALL be deleted in the same transaction
- **AND** rows in `edges` belonging to the flushed repo prefix that are absent from the staging table SHALL be deleted in the same transaction
- **AND** staging tables SHALL be dropped
- **AND** existing data from other repos SHALL be preserved
- **AND** the transaction SHALL be committed
- **AND** `s.bulk` SHALL be set to nil

#### Scenario: Non-destructive merge is idempotent
- **WHEN** a repo is re-indexed (warm restart) via the non-destructive merge path
- **AND** the live tables already contain rows for this repo's prefix
- **THEN** node rows SHALL be upserted (updated on ID conflict)
- **AND** edge rows SHALL be silently skipped on conflict
- **AND** rows for this repo prefix that no longer exist in the new index SHALL be removed
- **AND** the final state SHALL be identical to a fresh index

### Requirement: FlushBulk short-circuits on empty buffers

`FlushBulk` SHALL return without touching the database when no rows were buffered, and SHALL return an error when called without an active bulk-load session.

#### Scenario: FlushBulk with empty buffers
- **WHEN** a bulk-load session is active but no rows were buffered
- **AND** `FlushBulk` is called
- **THEN** the method SHALL return nil without modifying the database or starting a transaction
- **AND** `s.bulk` SHALL be set to nil

#### Scenario: FlushBulk without BeginBulkLoad
- **WHEN** no bulk-load session is active
- **AND** `FlushBulk` is called
- **THEN** the method SHALL return an error

### Requirement: Indexer wraps non-shadow path in bulk mode

The indexer's `IndexCtx` SHALL wrap the non-shadow parse path in `BeginBulkLoad`/`FlushBulk` when the graph store implements `BulkLoader`, extending COPY FROM benefits to warm restarts and multi-repo non-first indexes.

#### Scenario: Non-shadow path enters bulk mode
- **WHEN** the shadow-swap was NOT taken for this indexing pass
- **AND** the graph store implements `graph.BulkLoader`
- **THEN** `BeginBulkLoad` SHALL be called before the worker pool starts
- **AND** `FlushBulk` SHALL be called after all files are processed (in a defer, gated on `retErr == nil`)

#### Scenario: Shadow-swap path already handles bulk mode
- **WHEN** the shadow-swap WAS taken for this indexing pass
- **THEN** the existing drain defer SHALL handle `BeginBulkLoad`/`FlushBulk` as before
- **AND** no additional bulk wrapper SHALL be added

### Requirement: Bulk staging tables are per-process unique and leftovers are collected

Bulk staging table names MUST embed the writer's process id and a per-flush nonce. A flush MUST NOT fail because a staging table from a crashed or concurrent process exists. `BeginBulkLoad` SHALL best-effort drop leftover staging tables matching the staging name pattern.

#### Scenario: Leftover staging table from a crashed writer
- **WHEN** a previous writer crashed leaving a committed staging table matching the staging name pattern
- **AND** a new writer runs `BeginBulkLoad` and `FlushBulk`
- **THEN** the new flush SHALL succeed using its own unique staging names
- **AND** the leftover staging table SHALL be dropped by the leftover sweep

#### Scenario: Two concurrent flushes do not collide on staging names
- **WHEN** two bulk flushes run concurrently against the same schema
- **THEN** each SHALL create staging tables under distinct names
- **AND** neither SHALL fail with `42P07` (relation already exists) on staging creation
