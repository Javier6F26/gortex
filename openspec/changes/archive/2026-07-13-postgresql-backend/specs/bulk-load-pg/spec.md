## ADDED Requirements

### Requirement: PostgreSQL bulk-load fast path
The PostgreSQL backend SHALL implement `graph.BulkLoader` using `COPY FROM` into `UNLOGGED` tables as the high-throughput cold-load path.

#### Scenario: BeginBulkLoad starts bulk mode
- **WHEN** the indexer starts a cold parse phase
- **THEN** the backend enters bulk-load mode, accepting buffered writes

#### Scenario: AddBatch buffers rows during bulk mode
- **WHEN** AddBatch is called between BeginBulkLoad and FlushBulk
- **THEN** the backend buffers the nodes and edges for efficient commit

#### Scenario: FlushBulk commits via COPY FROM
- **WHEN** FlushBulk is called
- **THEN** the backend commits all buffered rows using PostgreSQL COPY FROM into UNLOGGED staging tables, creates indexes, swaps tables atomically, and returns to normal write mode

#### Scenario: Reads during bulk mode return available data only
- **WHEN** a read arrives between BeginBulkLoad and FlushBulk
- **THEN** the backend returns whatever data is visible (typically nothing from the buffer), as the resolver must not run until FlushBulk

#### Scenario: Bulk load is idempotent on node ID
- **WHEN** the same node ID is written twice during bulk load
- **THEN** the second write replaces the first (MERGE-on-PK semantics)

#### Scenario: FlushBulk on empty buffer is a no-op
- **WHEN** FlushBulk is called with no buffered writes since BeginBulkLoad
- **THEN** the backend returns without error

### Requirement: Normal write mode falls back to batched INSERT
The PostgreSQL backend SHALL support per-batch writes outside bulk-load mode using chunked INSERT statements with `synchronous_commit = OFF` for acceptable throughput.

#### Scenario: AddBatch commits via chunked INSERTs
- **WHEN** the resolver calls AddBatch outside a bulk-load bracket
- **THEN** the backend commits the batch via chunked INSERT statements with explicit BEGIN/COMMIT per chunk
