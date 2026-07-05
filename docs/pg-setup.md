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
