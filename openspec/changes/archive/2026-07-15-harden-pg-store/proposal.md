# Proposal: harden-pg-store

## Why

A code audit of `internal/graph/store_pg/` against the planned deployment topology (one ephemeral writer process indexing into a shared PostgreSQL schema, N long-lived read-only daemon processes serving queries from it, optionally through PG read replicas or pgbouncer) found five defects that lose data, crash reader processes, or corrupt the shared schema. Three of them are live bugs that also affect the current single-daemon PG deployment: the bulk-load swap leaves the live `nodes`/`edges` tables UNLOGGED (not crash-safe, invisible to physical read replicas), the read path panics the whole process on any transient PG error, and `ensureSchema` can be tricked into re-running non-idempotent DDL by a single failed read of `schema_version`.

## What Changes

- **Bulk load produces LOGGED live tables**: the destructive `FlushBulk` swap converts staging tables to LOGGED before renaming them into place, so the graph survives PG crash recovery and replicates to physical standbys.
- **Bulk staging tables get unique per-process names** (`nodes_bulk_<pid>_<nonce>`), with `DROP TABLE IF EXISTS` cleanup, so a crashed or concurrent writer can never poison future flushes with a leftover fixed-name staging table.
- **The non-destructive merge path deletes stale rows**: after upserting from staging, rows belonging to the flushed repo that are absent from staging are deleted in the same transaction, closing the "merge path never deletes" correctness drift on re-index.
- **Read-path queries stop panicking on transient errors**: `panicOnFatal` call sites on query methods are replaced with bounded retry (for transient SQLSTATE classes: connection failures, admin shutdown, serialization, replica recovery conflicts) falling back to a logged error + empty result + a store-level health flag, keeping existing `graph.Store` method signatures. The silent-`nil` asymmetry in `queryNodes`/`queryEdges` (pool error → silent empty, rows error → panic) becomes uniform.
- **Schema management becomes safe under concurrency and privilege restriction**: `readSchemaVersion` propagates errors instead of reporting version 0; migrations run under a PG advisory lock; the V1 DDL becomes idempotent (`IF NOT EXISTS`) as its header already claims; a new read-only open mode never applies migrations and fails fast with a clear error on version mismatch.
- **Connection config hardened**: pool defaults gain `statement_timeout` / `lock_timeout` runtime params (overridable), and `docs/pg-setup.md` documents the DSN settings required behind pgbouncer (transaction pooling breaks connect-time `search_path` and pgx's extended-protocol statement cache).

## Capabilities

### New Capabilities
- `pg-read-resilience`: PostgreSQL read-path behavior under transient failures — bounded retry, no process panics, uniform degraded-result semantics, health surfacing, and connection timeout defaults.
- `pg-schema-safety`: schema version detection, migration locking and idempotency, and the read-only open mode that never executes DDL.

### Modified Capabilities
- `pg-bulk-load`: bulk-load output must be crash-safe and replica-visible (LOGGED), staging tables must be per-process unique, and the merge path must delete stale rows for the flushed repo. (Modifies requirements introduced by the in-flight `fix-pg-bulk-load` change.)

## Impact

- Affected code: `internal/graph/store_pg/bulk_load.go` (SET LOGGED, staging names, merge delete), `internal/graph/store_pg/store.go` (panicOnFatal call sites, retry helper, health flag), `internal/graph/store_pg/schema_version.go` (error propagation, advisory lock, read-only mode), `internal/graph/store_pg/schema.go` (idempotent DDL), `internal/graph/store_pg/config.go` (runtime params, read-only flag), `docs/pg-setup.md`.
- No `graph.Store` interface changes; no changes to SQLite or in-memory backends.
- Interacts with the in-progress `fix-pg-bulk-load` change (same file, `bulk_load.go`); the staging-name and SET LOGGED work must land on top of its merge-path implementation.
- Unblocks the planned `follow-mode` change (read-only follower daemons), which depends on the read-only open mode and non-panicking reads.
