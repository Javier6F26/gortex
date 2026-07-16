# Design: harden-pg-store

## Context

`internal/graph/store_pg` was written for a single daemon owning its schema. The audited defects all trace back to that assumption:

- **UNLOGGED live tables.** The destructive `FlushBulk` path creates `nodes_bulk`/`edges_bulk` as `CREATE UNLOGGED TABLE` and renames them over `nodes`/`edges` (`bulk_load.go:166-248`). UNLOGGED tables are truncated on PG crash recovery and are not shipped to physical read replicas, so the entire graph is one primary crash away from vanishing, and read-replica followers would see empty core tables.
- **`panicOnFatal` on the read path.** Every store error other than `pgx.ErrNoRows` panics the process (`store.go:78`), from ~55 call sites including all node/edge/stats query methods. On a hot standby, `canceling statement due to conflict with recovery` is routine traffic. Worse, `queryNodes`/`queryEdges` are asymmetric: a `pool.Query` failure silently returns nil (looks like "not found") while a mid-iteration `rows.Err()` panics — the same transient fault yields two different behaviors depending on timing.
- **Schema DDL can fire from any process.** `readSchemaVersion` swallows all errors and reports version 0 (`schema_version.go:32-39`), so one failed SELECT during `Open` sends the process into the migration loop. There is no advisory lock, the V1 DDL is not idempotent (37 `CREATE TABLE/INDEX` without `IF NOT EXISTS`, contradicting the file's own header comment), and a binary newer than the schema will apply migrations — including `DROP TABLE` — under live readers.
- **No statement/lock timeouts.** `openPool` sets no `statement_timeout`/`lock_timeout`, and every query runs on the store's background context, so a reader can block indefinitely behind the bulk swap's ACCESS EXCLUSIVE lock. Behind pgbouncer in transaction pooling, the connect-time `SET search_path` (`config.go:82-97`) and pgx's extended-protocol statement cache both break.

Constraint that shapes everything: `graph.Store` methods like `GetFileNodes` return no `error`, and changing the interface would touch three backends and hundreds of callers. This change keeps all signatures.

## Goals / Non-Goals

**Goals:**
- The graph survives PG crash recovery and is visible on physical read replicas after any bulk load.
- A transient PG error (failover, recovery conflict, connection reset, timeout) never crashes a daemon process; it degrades to a retried query or a logged, observable empty result.
- Only one process can ever run schema DDL, only deliberately, and re-running the DDL is harmless.
- A store can be opened in a mode that provably never writes, as the foundation for follower daemons.

**Non-Goals:**
- No `graph.Store` interface changes; no SQLite/in-memory backend changes.
- No cross-process write coordination beyond migrations (single-writer remains a deployment convention; the writer advisory lock ships with `follow-mode`).
- No fix for the non-transactional evict→reinsert visibility window (tracked as a `follow-mode`/v0.1 concern; this change only fixes durability and process-survival defects).
- No retry of *write* statements (writes stay fail-fast; only reads retry).

## Decisions

### D1 — `ALTER TABLE ... SET LOGGED` on staging before the rename, not LOGGED staging
Creating staging as LOGGED would WAL-log every COPY row, forfeiting most of the bulk-load win. Converting once at the end (`SET LOGGED` rewrites the table into WAL in one pass) keeps COPY fast and pays the WAL cost once, inside the existing swap transaction. Alternative rejected: leaving tables UNLOGGED and documenting "no replicas, snapshot backups only" — unacceptable because it also loses the graph on primary crash.

### D2 — Staging names suffixed with `<pid>_<nonce>`, plus `DROP TABLE IF EXISTS` on entry and rollback
Fixed names mean any leftover permanently poisons future flushes (`42P07`) until a human drops it. Unique names make concurrent/crashed writers inert; a startup sweep (`DROP TABLE IF EXISTS` for tables matching `nodes_bulk_%` older than a threshold via `pg_class`) garbage-collects leftovers. Alternative rejected: PG temp tables — they are session-local, which breaks the COPY-then-swap flow across pooled connections.

### D3 — Merge path deletes stale rows via anti-join in the same transaction
After the `INSERT ... ON CONFLICT` upserts, run `DELETE FROM nodes WHERE repo_prefix = $1 AND id NOT IN (SELECT id FROM <staging>)` (and the edge equivalent keyed on the repo's node ids / file prefix) inside the same tx, so readers see the repo replaced atomically at commit. Alternative rejected: `EvictRepo` before flush — it is autocommit-per-chunk today, which would widen the zero-rows window this change is trying not to introduce.

### D4 — Read resilience: bounded retry + uniform degraded result + health flag, signatures unchanged
A single helper classifies retryable SQLSTATEs (class 08, `57P01`, `40001`, `40P01`, and standby recovery-conflict `40001`/`57014` when on a replica) and retries with short exponential backoff (e.g. 3 attempts, ~50/150/450ms). On exhaustion: log at WARN with the query tag, record into an atomic `lastReadErr`/counter exposed via a `Health()` accessor (surfaced later by `daemon_health`/`/healthz`), and return the zero value. `panicOnFatal` remains only for programming errors (nothing today qualifies → it becomes unreachable on reads and is removed from read paths). `queryNodes`/`queryEdges` get the same treatment for both their failure points, ending the silent-nil/panic asymmetry. Alternative rejected: adding `error` returns — interface-wide breakage; recover() middleware — hides the fault class and cannot retry.

### D5 — Schema safety: propagate, lock, idempotent, and a read-only mode
- `readSchemaVersion` returns its error; `ensureSchema` fails `Open` on it rather than migrating.
- Migrations run inside `pg_advisory_xact_lock(<constant key>)`, so concurrent Opens serialize and losers re-read the version and no-op.
- V1 DDL gets `IF NOT EXISTS` everywhere (making the header comment true); migrations stay single-statement-batch as today.
- `Config.ReadOnly` (new): `ensureSchema` only *reads* the version — on mismatch (stored < current or stored > current) it returns a typed error telling the operator to run the writer first. `ReadOnly` also arms the write-guard groundwork: every mutating store method returns `ErrReadOnlyStore` (methods without error returns log-and-drop and flip the health flag). Putting the guard inside `store_pg` (not a wrapper around `graph.Store`) preserves the optional-capability type assertions (`ContentSearcher`, `VectorSearcher`, `BFSCapable`, …) that a wrapper would silently break.

### D6 — Pool runtime params + pgbouncer guidance
`openPool` gains default `RuntimeParams`: `statement_timeout` (default 30s) and `lock_timeout` (default 5s), both overridable via `Config`/DSN. Documentation (not code) covers pgbouncer: transaction pooling requires `default_query_exec_mode=exec` in the DSN and either session pooling or schema-qualified access instead of the connect-time `search_path`. Alternative rejected: auto-detecting pgbouncer — fragile, and the failure mode (empty results from the wrong schema) is too quiet to risk heuristics.

## Risks / Trade-offs

- [`SET LOGGED` rewrite time extends the swap transaction and its ACCESS EXCLUSIVE window] → readers now have `lock_timeout` (D6) so they fail fast and retry (D4) instead of stalling; the swap path only runs on cold/empty schemas where reader traffic is minimal.
- [Retry-on-read can triple worst-case latency during an outage] → attempts and backoff are constants tuned low; the health flag makes persistent degradation observable rather than silent.
- [Degraded empty results are still empty results] → unavoidable without interface changes; mitigation is the health flag + WARN logs + (in `follow-mode`) `/healthz` wiring, so operators can distinguish "no data" from "store degraded".
- [`DELETE ... NOT IN (staging)` anti-join cost on very large repos] → use `NOT EXISTS`, which the planner resolves as a hash anti-join (no staging index required). **Measured (100k live rows, 90k staging, 10k deleted): ~31 ms.** The `starts_with(from_id, prefix)` edge scope avoids LIKE-wildcard escaping.

**Measured overhead (task 1.8, 100k rows, pgvector/pg18, local):**
- `ALTER TABLE … SET LOGGED` (destructive swap, one WAL-logged rewrite pass): **~211 ms / 100k rows** (~2 ms per 1k). Runs only on the cold/empty-schema destructive path, once, inside the swap transaction.
- Merge-path stale-row anti-join `DELETE`: **~31 ms / 100k rows**. Runs once per merge flush.
- No dedicated pg bulk-perf assertion harness exists (`GORTEX_BULK_PERF_ASSERT` is sqlite-only); the numbers above were characterized directly against a live PostgreSQL.
- [Same-file collision with in-progress `fix-pg-bulk-load`] → sequencing: land after its store-layer tasks (already complete) and rebase this change's bulk_load.go edits on top; its remaining tasks (5.3–5.5) are manual verification and do not conflict.

## Migration Plan

No data migration. Existing UNLOGGED live tables (from prior destructive swaps) are converted by a one-time `ALTER TABLE nodes SET LOGGED` / `edges` in a V3 schema migration, so already-deployed databases become crash-safe on first writer boot after upgrade. Rollback: revert the binary; the V3 migration is harmless to keep.

## Open Questions

- Should the retry classifier treat `57014` (query_canceled) as retryable always, or only when the store knows it is pointed at a standby? Default: retry always, capped attempts make it cheap.
- Exact advisory lock key constant (must be documented and stable across versions; proposal: `hashtext('gortex_schema_migration')` equivalent fixed int64).
