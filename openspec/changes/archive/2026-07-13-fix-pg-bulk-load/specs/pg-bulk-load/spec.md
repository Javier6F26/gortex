## ADDED Requirements

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

When `tableEmpty` is true, `FlushBulk` SHALL use the destructive table-swap path: create UNLOGGED staging tables, COPY FROM into them, build indexes, and atomically swap staging tables with live tables.

#### Scenario: Destructive swap on first repo
- **WHEN** a bulk-load session is active with `tableEmpty: true` and buffered rows
- **AND** `FlushBulk` is called
- **THEN** UNLOGGED staging tables SHALL be created with `LIKE {nodes,edges} INCLUDING ALL`
- **AND** buffered rows SHALL be written to staging tables via COPY FROM
- **AND** indexes SHALL be built on staging tables
- **AND** live tables SHALL be atomically replaced via ALTER TABLE RENAME
- **AND** old tables SHALL be dropped
- **AND** the transaction SHALL be committed
- **AND** `s.bulk` SHALL be set to nil

### Requirement: FlushBulk non-destructive merge path for non-empty table

When `tableEmpty` is false, `FlushBulk` SHALL use the non-destructive merge path: create UNLOGGED staging tables without indexes, COPY FROM into them, and INSERT INTO SELECT into the live tables with ON CONFLICT handling.

#### Scenario: Non-destructive merge on subsequent repos
- **WHEN** a bulk-load session is active with `tableEmpty: false` and buffered rows
- **AND** `FlushBulk` is called
- **THEN** UNLOGGED staging tables SHALL be created with `LIKE {nodes,edges}` (no INCLUDING clause, no indexes)
- **AND** buffered rows SHALL be written to staging tables via COPY FROM
- **AND** nodes SHALL be merged into the live `nodes` table via `INSERT INTO nodes SELECT * FROM nodes_bulk ON CONFLICT (id) DO UPDATE SET` with the full `nodeInsertConflict` clause
- **AND** edges SHALL be merged into the live `edges` table via `INSERT INTO edges SELECT * FROM edges_bulk ON CONFLICT (from_id, to_id, kind, file_path, line) DO NOTHING`
- **AND** staging tables SHALL be dropped
- **AND** existing data from other repos SHALL be preserved
- **AND** the transaction SHALL be committed
- **AND** `s.bulk` SHALL be set to nil

#### Scenario: Non-destructive merge is idempotent
- **WHEN** a repo is re-indexed (warm restart) via the non-destructive merge path
- **AND** the live tables already contain rows for this repo's prefix
- **THEN** node rows SHALL be upserted (updated on ID conflict)
- **AND** edge rows SHALL be silently skipped on conflict
- **AND** the final state SHALL be identical to a fresh index

### Requirement: FlushBulk short-circuits on empty buffers

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
