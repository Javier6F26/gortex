# Tasks: harden-pg-store

## 1. Bulk load: LOGGED tables + unique staging + merge deletes

- [x] 1.1 Rebase on the current `fix-pg-bulk-load` state of `internal/graph/store_pg/bulk_load.go` (its store-layer tasks are complete; only manual-verify tasks remain)
- [x] 1.2 Replace fixed staging names with per-process unique names (`nodes_bulk_<pid>_<nonce>`, `edges_bulk_<pid>_<nonce>`) in both FlushBulk paths; thread the names through COPY/merge/drop statements
- [x] 1.3 Add a best-effort leftover sweep in `BeginBulkLoad`: drop tables matching the staging name pattern (query `pg_class`/`information_schema` for `nodes_bulk_%`/`edges_bulk_%`)
- [x] 1.4 Destructive path: add `ALTER TABLE <staging> SET LOGGED` for nodes and edges staging after index build, before the RENAME
- [x] 1.5 Merge path: after the ON CONFLICT upserts and inside the same transaction, delete rows for the flushed repo prefix absent from staging (`NOT EXISTS` anti-join against the staging PK for nodes; matching cleanup for edges)
- [x] 1.6 Schema migration V3: one-time `ALTER TABLE nodes SET LOGGED` / `ALTER TABLE edges SET LOGGED` guarded to no-op when already LOGGED, so existing deployments become crash-safe on upgrade
- [x] 1.7 Tests: staging-name uniqueness (two concurrent flushes), leftover sweep, `relpersistence = 'p'` after destructive swap, merge-path stale-row deletion (re-index removes vanished nodes/edges), merge idempotence preserved
- [x] 1.8 Run the bulk perf assertion (`GORTEX_BULK_PERF_ASSERT`) and record the SET LOGGED + anti-join overhead in the change notes

## 2. Read resilience: retry + degrade instead of panic

- [x] 2.1 Add a `retryableSQLState(err)` classifier (class 08, `57P01`, `40001`, `40P01`, `57014`, recovery-conflict) and a `withReadRetry(tag, fn)` helper with bounded backoff (3 attempts, ~50/150/450ms) in `internal/graph/store_pg`
- [x] 2.2 Add store health state: atomic degraded-read counter + last-error, exposed via a `Health()` accessor
- [x] 2.3 Convert `queryNodes`/`queryEdges` to route both failure points (pool.Query error AND rows.Err) through retry → WARN log → health record → zero value; remove the silent-nil asymmetry
- [x] 2.4 Sweep the remaining direct `panicOnFatal` read call sites (GetNode, GetNodeByQualName, counts, Stats, RepoStats, memory estimates, batched lookups, EdgesByKind/NodesByKind) onto the same helper; keep writes fail-fast (error returns, no retry)
- [x] 2.5 Delete `panicOnFatal` from all read paths; document that any remaining use is a programming-error guard only
- [x] 2.6 Failure-injection tests: cancel/kill the backend connection mid-iteration and at query start; assert no panic, retry-then-succeed, and degraded-empty + health flag on exhaustion (use a real PG via the existing store_pg test harness; skip when no DSN)
- [x] 2.7 Wire `Health()` into `daemon_health` payload so degraded reads are visible in `gortex daemon status`

## 3. Schema safety

- [x] 3.1 `readSchemaVersion`: return the query error; `ensureSchema` fails `Open` on it (no version-0 fallback)
- [x] 3.2 Wrap the migration loop in `pg_advisory_xact_lock(<fixed documented key>)`; re-read the stored version after acquiring the lock before applying anything
- [x] 3.3 Make `schemaSQL` idempotent: `IF NOT EXISTS` on every `CREATE TABLE` / `CREATE INDEX`; fix the header comment to match reality
- [x] 3.4 Add `Config.ReadOnly`: skip migrations entirely; on stored≠expected version return a typed `ErrSchemaVersionMismatch` naming the writer as the fix
- [x] 3.5 Read-only write guard inside `store_pg`: every mutating method returns `ErrReadOnlyStore` (error-returning signatures) or logs-and-drops + health flag (void signatures); table-drive the method list against the `graph.Store` mutation surface
- [x] 3.6 Tests: concurrent Opens on empty schema (one migrates, rest no-op, no 42P07), version-read error fails Open with no DDL, read-only open succeeds on current schema / fails typed on mismatch, every mutating method refuses in read-only mode, read capabilities still assert and serve

## 4. Connection config + docs

- [x] 4.1 `openPool`: default `RuntimeParams` `statement_timeout=30s`, `lock_timeout=5s`, overridable via `Config` fields and DSN
- [x] 4.2 Document pgbouncer guidance in `docs/pg-setup.md`: transaction pooling requires `default_query_exec_mode=exec` and schema-qualified access (or session pooling) because connect-time `search_path` and the pgx statement cache break
- [x] 4.3 Document the advisory lock key, the read-only mode, and the health accessor in `docs/pg-setup.md`

## 5. Verify

- [x] 5.1 `go test -race ./internal/graph/store_pg/...` green (with and without a PG DSN available)
- [x] 5.2 `go test -race ./...` green
- [x] 5.3 Manual: cold-index a multi-repo workspace into a clean PG, crash PG (`pg_ctl stop -m immediate`), restart, confirm graph intact (`nodes`/`edges` row counts unchanged)
- [x] 5.4 Manual: point a second read-only daemon at a physical read replica and confirm node/edge/search queries return the writer's data
