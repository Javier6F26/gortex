## Why

Gortex's graph store currently supports two backends: in-memory and SQLite (via `modernc.org/sqlite`). While SQLite works well for single-user, embedded scenarios, it lacks the concurrency, network accessibility, operational tooling, and analytical query power that PostgreSQL provides. Adding a PostgreSQL backend unlocks:

- **Multi-process / multi-host access** to the same graph store (daemon + CLI + CI runners sharing one database)
- **Managed infrastructure** via RDS, Aurora, Supabase, or Neon — no local file management, WAL checkpoints, or disk quotas
- **Rich analytical queries** with `pgvector` (ANN vector search replacing brute-force), `pg_trgm` (fuzzy symbol search), and full SQL analytics on the graph
- **Operational visibility** — standard tooling (pgAdmin, DataGrip, Grafana) instead of SQLite-specific PRAGMAs
- **Team deployments** where each developer's daemon in a CI/CD pipeline reads from the same durable store

## What Changes

- **New `internal/graph/store_pg/` package** — PostgreSQL implementation of `graph.Store` and all ~38 optional capability interfaces (SymbolSearcher, ContentSearcher, VectorSearcher, BFSCapable, BackendResolver, aggregators, sidecars, etc.)
- **`gortex daemon --backend postgres --pg-dsn <dsn>`** — new daemon flag to select the PostgreSQL backend at startup
- **`gortex repos`** — updated to read index state from PostgreSQL when the daemon uses it
- **`internal/serverstack/backend.go`** — extended switch with `case "postgres"`, plus pool-based connection management replacing the embedded WAL + flock
- **`internal/serverstack/shared_server.go`** — cross-process flock gated behind `isEmbeddedBackend()` instead of `!isSqliteBackend()` so network backends don't take a pointless file lock
- **`internal/graph/store_sqlite/` stays untouched** — SQLite remains the default and is fully supported alongside the new backend. No breaking changes.
- **Dependency added**: `github.com/jackc/pgx/v5` (PostgreSQL driver + pool), `github.com/pgvector/pgvector-go` (vector extension client)

## Capabilities

### New Capabilities

- `symbol-search-pg`: Full-text search over symbol names using PostgreSQL `pg_trgm` extension with GIN indexes and similarity ranking
- `content-search-pg`: Full-text search over content section bodies using PostgreSQL `tsvector` with GIN indexes and `ts_rank` scoring
- `vector-search-pg`: Approximate nearest-neighbor (ANN) vector search using `pgvector` with HNSW indexes, replacing the current in-memory brute-force O(N) path
- `graph-traversal-pg`: Recursive CTE-based BFS, reachability, and class hierarchy traversal in PostgreSQL
- `graph-aggregators-pg`: Server-side push-down aggregations (GROUP BY, COUNT, IN-list filters) replacing Go-side materialization
- `graph-sidecars-pg`: Durable sidecar tables (file_mtimes, ref_facts, clone_shingles, enrichment tables, constants, etc.)
- `bulk-load-pg`: High-throughput cold-load path using `COPY FROM` + `UNLOGGED` tables for PostgreSQL
- `backend-wiring-pg`: CLI flags, connection pooling, schema migration, and lifecycle management for the PostgreSQL backend

### Modified Capabilities

- _(none — no existing capabilities have their requirements changed)_

## Impact

- **New package**: `internal/graph/store_pg/` (~28 files, ~8,000 lines)
- **Modified packages**: `internal/serverstack/` (backend.go, shared_server.go), `cmd/gortex/` (daemon.go, repos_cmd.go), `internal/daemon/paths.go`
- **New dependency**: `github.com/jackc/pgx/v5`, `github.com/pgvector/pgvector-go`, `github.com/golang-migrate/migrate` (optional)
- **Database extensions required**: `pg_trgm` (or `pgvector`) on the target PostgreSQL instance
- **No breaking changes** to existing SQLite users — all existing behavior preserved
- **Snapshot semantics change**: PostgreSQL backend does not use gob+gzip snapshots (the database IS the durable store). The memory backend still snapshots.
