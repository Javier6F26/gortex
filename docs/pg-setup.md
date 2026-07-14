# PostgreSQL Backend Setup Guide

Gortex can use PostgreSQL as its graph store backend, replacing the default
embedded SQLite. This enables multi-process access, managed infrastructure
(RDS, Aurora, Supabase, Neon), and rich analytical queries via `pg_trgm`,
`tsvector`, and `pgvector`.

> **Prerequisite**: A running PostgreSQL instance (14+) with the `pg_trgm`
> and `pgvector` extensions available. The extensions are created
> automatically by Gortex on first use.

---

## Quick start

```bash
# 1. Start the daemon with the PostgreSQL backend
gortex daemon start --backend postgres \
  --pg-dsn "postgres://user:pass@localhost:5432/gortex?sslmode=disable"

# 2. Verify it's running
gortex daemon status

# 3. Index a repo (or let warmup handle tracked repos)
gortex track .
```

---

## Connection string format

The `--pg-dsn` flag accepts standard PostgreSQL connection URIs:

```
postgres://[user[:password]@][host][:port][/dbname][?param1=val1&...]
```

### Common examples

| Environment | DSN |
|---|---|
| Local (default port 5432) | `postgres://localhost:5432/gortex?sslmode=disable` |
| Local (non-default port 5433) | `postgres://localhost:5433/gortex_test?sslmode=disable` |
| Local (Unix socket) | `postgres:///gortex?host=/var/run/postgresql` |
| Authenticated | `postgres://user:secret@localhost:5432/gortex` |
| RDS / Aurora | `postgres://user:pass@my-cluster.rds.amazonaws.com:5432/gortex?sslmode=require` |
| Supabase | `postgres://postgres:password@db.xxxxx.supabase.co:5432/postgres?sslmode=require` |
| Neon | `postgres://user:pass@ep-xxxxx.us-east-2.aws.neon.tech/neondb?sslmode=require` |

> **Note**: Passwords containing special characters must be URL-encoded
> (`%` → `%25`, `@` → `%40`, etc.).

---

## Required PostgreSQL extensions

Gortex requires two PostgreSQL extensions:

| Extension | Purpose |
|---|---|
| `pg_trgm` | Trigram-based fuzzy symbol name search with typo tolerance |
| `pgvector` | HNSW-accelerated approximate nearest-neighbour vector search |

Gortex attempts to create these automatically on the first connection as
part of its schema migration. On most local installations this Just Works.

If the `CREATE EXTENSION` fails (e.g. insufficient permissions on managed
platforms), Gortex reports the error and stops — install the extensions
manually, then restart:

```sql
CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;
CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;
```

> **Note**: On some managed PostgreSQL platforms (RDS, Supabase, Neon),
> extension creation may require superuser or specific roles. Running the
> `CREATE EXTENSION` statements manually before starting Gortex is always
> safe — the schema migration is idempotent and skips them if already present.

---

## Connection pool tuning

The pool is configured via `--pg-pool-size` (default: `NumCPU * 2`). Tune
for your workload:

| Workload | Recommended pool size | Rationale |
|---|---|---|
| Single-user development | 4–8 | Low concurrency; minimize connection churn |
| Multi-session daemon | `NumCPU * 2` (default) | Parallel enrichment + resolvers |
| High-throughput CI | 16–32 | Many concurrent index + query sessions |
| Production (heavy query) | `NumCPU * 4` | Query parallelism + background enrichment |

Additional pool settings (configurable via `store_pg.Config` when
embedding):

- **MaxConnLifetime**: 30 minutes (default). Connections are recycled
  after this age to handle DNS changes and load balancer rotation.
- **HealthCheckPeriod**: 30 seconds (default). Background health check
  pings idle connections.
- **StatementTimeout**: 30 seconds (default). Sets the `statement_timeout`
  runtime parameter on every pooled connection, so a runaway query fails
  within a bounded time instead of stalling.
- **LockTimeout**: 5 seconds (default). Sets the `lock_timeout` runtime
  parameter, so a reader waiting behind an exclusive table lock (e.g. the
  bulk-load swap's `ACCESS EXCLUSIVE`) fails fast rather than hanging. The
  failure is transient and is retried automatically by the read path.

Both timeouts are overridable: an explicit `StatementTimeout` /
`LockTimeout` in `store_pg.Config` wins; otherwise a `statement_timeout` /
`lock_timeout` present in the DSN is honored; otherwise the defaults
apply. Set them to `-1` in the DSN (`?options=-c%20statement_timeout%3D0`)
only if you deliberately want no bound.

---

## Read resilience and health

Read-path queries never panic on a transient PostgreSQL error. Transient
failures — connection-exception class `08`, admin/crash shutdowns
(`57P01`/`57P02`/`57P03`), `40001` serialization_failure (the SQLSTATE of a
standby recovery conflict), `40P01` deadlock_detected, `57014`
query_canceled, `55P03` lock_not_available (a `lock_timeout` expiry), and
reset/dropped connections — are retried with bounded exponential backoff
(3 attempts, ≈50/150/450 ms). On retry exhaustion the read logs at WARN,
increments a store health counter, and returns its zero value (empty
slice / nil node / zero count) so the process stays up.

The store exposes a **health accessor** (`(*store_pg.Store).Health()`,
satisfying `graph.StoreHealthReporter`) reporting the degraded-read count,
the read-only write-refusal count, and the most recent error. It is
surfaced in `notifications/daemon_health` (visible via
`gortex daemon status`) as `store_degraded_reads`, `store_write_refusals`,
`store_last_error`, and `store_last_error_unix`, so operators can tell
"no data" apart from "store degraded".

---

## Schema migrations and the advisory lock

Schema migrations run inside a PostgreSQL advisory lock with the fixed key
**`0x676F72746D696772`** (decimal `7,453,301,752,698,857,330`; the ASCII
of `gortmigr`). Concurrent `Open` calls serialize on this key: the winner
applies pending migrations in a single transaction, and every other
process blocks, re-reads the version, and no-ops when the schema is
already current — so N daemons opening a blank database never race into
duplicate `CREATE` statements. The key is stable across Gortex versions;
do not reuse it for other application advisory locks.

A failed read of the `schema_version` table (a connection blip, a
permission error) now fails `Open` instead of being misread as "blank
database" and re-running DDL. A genuinely blank database (the table simply
does not exist) is still detected and bootstrapped.

---

## Read-only mode (follower daemons)

Set `store_pg.Config.ReadOnly = true` to open the store against a schema
another process owns — for example a follower daemon pointed at a physical
read replica. A read-only store:

- **never executes DDL.** If the stored schema version differs from the
  version this binary expects, `Open` fails with a typed
  `*store_pg.SchemaVersionMismatchError` (matchable with
  `errors.Is(err, store_pg.ErrSchemaVersionMismatch)`) telling you to run a
  writer-mode process first. Run the writer once to migrate, then start the
  read-only followers.
- **refuses every write.** Mutating methods with an error return yield
  `store_pg.ErrReadOnlyStore`; void mutators drop the write, log at WARN,
  and increment `store_write_refusals` in the health accessor.
- **keeps all read capabilities.** The optional-capability type assertions
  (`ContentSearcher`, `VectorSearcher`, `BFSCapable`, aggregators, …)
  continue to succeed and serve queries.

Point a read-only follower at a hot standby and node/edge/search queries
return the writer's data. Recovery-conflict cancellations on the standby
are transient and handled by the read-resilience retry path above.

---

## Follow mode: one writer + N diskless followers

`gortex daemon start --follow` runs a **read-only follower**: a diskless
daemon that serves the full read-tool surface from a shared PostgreSQL
schema without cloning repos, indexing, or watching the filesystem. This
is the read plane of a writer-as-job topology:

- **Write plane** — one indexing daemon (or an ephemeral job): clone repos,
  `gortex track --wait` each, index into the schema, stop. It holds the
  **writer advisory lock** (`pg_advisory_lock`, keyed on a fixed constant
  XORed with the schema identity) for its lifetime, so a second writer — or
  an accidental normal daemon on the same DSN — fails startup with a clear
  "another writer holds this schema" error (naming the holder PID). The
  lock releases automatically on exit or crash (session-scoped).
- **Read plane** — N stateless `--follow` daemons behind a load balancer,
  optionally against PG read replicas. Followers never take the writer lock.

### What a follower does and does not do

- Opens the store **read-only** (`Config.ReadOnly`): every mutating method
  is refused at the store layer (the backstop of a three-layer write seal).
- Constructs **no MultiIndexer**: no repo tracking, no warmup reconcile
  (which would evict "missing" files from the shared schema — the reason a
  vanilla daemon must never be pointed at a shared schema diskless), no
  watcher, janitor, snapshotter, enrichment, embedder, or LSP subprocess.
- Publishes readiness directly (`snapshot_loaded → ready`) in seconds and
  serves live SQL reads — a follower sees the writer's committed rows
  immediately, with **no reload machinery**.
- **Seals writes at three layers**: (1) the tool preset is forced to
  `readonly`/`hide` (not widenable by `GORTEX_TOOLS`); (2) the control-channel
  RPCs `track`/`untrack`/`reload`/`reload-servers` return a typed
  `follow_mode` error; (3) the read-only store guard. Residual graph-writers
  (rationale projection, co-change prewarm, federation proxy hydration, the
  `ensureFresh` read-path self-heal) are made inert; `post_review` and
  `feedback` are denied.
- Serves **source without disk**: code files come byte-exact from the
  content-addressed `file_blobs` table; markdown/document files are
  reconstructed from the graph store (marked `source: "store"`). Git-
  dependent tools (diff/review/PR) return a typed `follow_no_disk` error.

### Deploy order

1. Roll the writer binary and run one indexing cycle so the schema is at the
   current version, LOGGED, and blob-populated:
   ```bash
   gortex daemon start --backend postgres --pg-dsn "$DSN"
   gortex track /path/to/repo --wait      # per repo
   gortex daemon stop
   ```
2. Start followers (any number), each diskless:
   ```bash
   gortex daemon start --follow --backend postgres --pg-dsn "$DSN" \
     --http-addr 0.0.0.0:7411 --http-auth-token "$TOKEN"
   ```
3. `gortex daemon status` on a follower shows `mode: follow` and the
   freshness lag (now − newest `repo_index_state.indexed_at`). `/healthz`
   returns non-200 on persistent store-read degradation, and — when
   `GORTEX_FOLLOW_MAX_LAG` (a Go duration, off by default) is set — when the
   lag exceeds it. These are what k8s readiness/liveness probes consume.

### Read-replica and pgbouncer caveats

- Followers may point at a **physical read replica**. `file_blobs` and the
  core tables are LOGGED (see the harden-pg-store migration), so they
  replicate. Recovery-conflict cancellations on the standby are transient
  and retried automatically.
- A **pre-blob schema** (indexed before this version) has no `file_blobs`
  rows; code source reads then return `follow_no_disk` — re-run the writer
  to populate blobs. Markdown/document reads still work (reconstructed from
  graph nodes).
- Behind **pgbouncer**, follow the transaction-pooling guidance below
  (`default_query_exec_mode=exec` + schema-qualified access or session
  pooling).

---

## Running behind pgbouncer

pgbouncer in **transaction pooling** mode breaks two things Gortex relies
on by default:

1. **Connect-time `search_path`.** Gortex sets `search_path` once per
   physical connection (via `Config.Schema`). Under transaction pooling a
   logical connection is not pinned to one physical connection, so that
   `SET` does not persist. Either use **session pooling**, or drop
   `Config.Schema` and rely on **schema-qualified** access / a
   `search_path` baked into the pgbouncer user's role default.
2. **pgx's extended-protocol statement cache.** Prepared statements do not
   survive transaction pooling. Add **`default_query_exec_mode=exec`** to
   the DSN so pgx uses the simple query protocol:

   ```
   postgres://user:pass@pgbouncer:6432/gortex?default_query_exec_mode=exec
   ```

Auto-detecting pgbouncer is intentionally not attempted — the failure mode
(empty results from the wrong schema) is too quiet to risk a heuristic.
When in doubt, use **session pooling**, which behaves like a direct
connection.

---

## Schema isolation (testing)

During tests, each test case gets its own PostgreSQL schema for
isolation. The DSN is set via `GORTEX_TEST_PG_DSN`:

```bash
export GORTEX_TEST_PG_DSN="postgres://localhost:5433/gortex_test?sslmode=disable"
go test -race ./internal/graph/store_pg/...
```

If `GORTEX_TEST_PG_DSN` is unset, tests default to
`postgres://localhost:5433/gortex_test?sslmode=disable` — matching the
`docker-compose.yml` pg test port.

---

## Backend comparison

| Feature | SQLite | PostgreSQL |
|---|---|---|
| Persistence | File-based (`.sqlite`) | Network database |
| Access model | Single-process | Multi-process / multi-host |
| Symbol FTS | FTS5 virtual table | pg_trgm GIN index |
| Content FTS | FTS5 virtual table | tsvector GIN index |
| Vector search | In-memory brute-force O(N) | pgvector HNSW ANN O(log N) |
| Graph traversal | Go-side walk | Recursive CTE |
| Bulk load | Drop indexes + sync=OFF | COPY FROM + UNLOGGED tables |
| Setup | Zero (embedded) | Requires PostgreSQL instance |
| Setup guide | — | This document |

---

## Troubleshooting

### "pq: password authentication failed"

Verify the password in the DSN is URL-encoded if it contains special
characters. Check `pg_hba.conf` allows the connection method you're
using (md5 / scram-sha-256 for password auth, trust for local sockets).

### "could not connect to server: Connection refused"

Ensure PostgreSQL is running and accepting connections on the expected
host/port. For Docker: `docker compose up -d` (see `docker-compose.yml`
at the repo root).

### "type "vector" does not exist" / "extension "pg_trgm" not available"

Gortex tries to install both extensions on first connection. If that fails,
install them manually:

1. **pg_trgm** — Install the `postgresql-contrib` package for your
   PostgreSQL version, or use a managed platform that bundles contrib
   extensions (RDS, Aurora, Supabase, Neon all do).
2. **pgvector** — Install the extension:

   | Platform | Command / Image |
   |---|---|
   | Linux | `apt install postgresql-16-pgvector` (adjust version) |
   | macOS | `brew install pgvector` |
   | Docker | `pgvector/pgvector:pg16` image |
   | Managed | RDS / Aurora / Supabase / Neon all support pgvector natively |

Then run the SQL manually and restart Gortex:

```sql
CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;
CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;
```

---

## Environment variables

| Variable | Overrides | Default |
|---|---|---|
| `GORTEX_TEST_PG_DSN` | Test DSN | `postgres://localhost:5433/gortex_test?sslmode=disable` |
