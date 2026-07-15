## Context

Gortex currently provides two graph.Store implementations:

- **in-memory `*graph.Graph`** — the reference implementation, nanosecond reads, zero persistence
- **`store_sqlite.Store`** — pure-Go SQLite via `modernc.org/sqlite`, ~12k lines across 28 files, implementing 38 interfaces (the core `Store` + 30+ optional capability interfaces)

The SQLite backend is deeply optimized for SQLite-specific features: FTS5 virtual tables, `WITHOUT ROWID` tables, `INSERT OR REPLACE`/`IGNORE` semantics, WAL mode with periodic checkpointing, and a bulk-load fast path that drops secondary indexes. While performant, it binds Gortex to an embedded-database model: single-process access, local file management, and no network queryability.

The `graph.Store` interface in `internal/graph/store.go` was designed from the start to support multiple backends — the package doc explicitly mentions "a remote network client" as a future implementation. The conformance test suite (`internal/graph/storetest`) validates every backend against the same contract.

This design adds PostgreSQL as a third backend, reusing the interface and test suite while translating SQLite-specific features to their PostgreSQL equivalents.

## Goals / Non-Goals

**Goals:**

- Implement `graph.Store` and all optional capability interfaces against PostgreSQL
- Support `gortex daemon --backend postgres --pg-dsn <dsn>` as a first-class option
- Preserve `store_sqlite` unchanged — zero regressions for existing SQLite users
- Use `pg_trgm` for symbol FTS (fuzzy name matching with typo tolerance)
- Use `tsvector` for content FTS (section bodies)
- Use `pgvector` with HNSW for ANN vector search
- Pass the full `storetest` conformance suite
- Keep the Go binary CGo-free (pgx is pure Go)

**Non-Goals:**

- Removing or deprecating SQLite support
- Migrating the sidecar store (`internal/persistence/sidecar_sqlite.go` — notes/memories) — it stays SQLite
- Real-time cross-daemon graph invalidation via LISTEN/NOTIFY (future work)
- Performance parity with SQLite on single-node workloads (network latency is inherently higher)
- Multi-tenant or sharded PostgreSQL deployments

## Decisions

### D1: Driver — pgx v5 with pgxpool

**Decision:** Use `github.com/jackc/pgx/v5` with `pgxpool` for connection pooling.

**Rationale:**
- Pure Go, no CGo (matches the project's CGo-free constraint for the SQLite store)
- De facto standard PostgreSQL driver in the Go ecosystem
- `pgxpool` provides connection multiplexing, health checks, and configurable pool size
- Native support for `COPY FROM` (needed for bulk load), `LISTEN`/`NOTIFY` (future), and array parameters
- `database/sql` compatibility layer is available but we use pgx-native for performance

**Alternatives considered:**
- `lib/pq` — maintained but legacy, no `pgxpool`, slower `COPY` performance
- `database/sql` with `pgx` stdlib adapter — loses pgx-native features like `COPY FROM`, `pgtype`

### D2: Symbol Full-Text Search — pg_trgm over tsvector

**Decision:** Use `pg_trgm` extension with GIN indexes for symbol name search.

**Rationale:**
- Symbol names are short identifiers (function names, class names, variable names) — not prose
- `pg_trgm` provides trigram similarity matching with typo tolerance (`similarity()`, `word_similarity()`)
- `SELECT ... WHERE name % 'query'` is simple and maps well to the existing FTS5 query semantics
- `strict_word_similarity()` handles camelCase and snake_case token boundaries naturally
- GIN indexes on `gin_trgm_ops` are efficient for the symbol corpus size (~70k nodes)
- `pg_trgm` is available on all major PostgreSQL platforms (RDS, Aurora, Supabase, Neon, self-hosted)

**SQL mapping:**

```sql
-- SQLite FTS5 (current)
SELECT node_id, tokens FROM symbol_fts WHERE symbol_fts MATCH ?;

-- PostgreSQL pg_trgm (new)
CREATE INDEX idx_symbols_name_trgm ON nodes USING GIN (name gin_trgm_ops);
SELECT id, name, similarity(name, $1) AS score
FROM nodes
WHERE name % $1
ORDER BY score DESC
LIMIT $2;
```

**Alternatives considered:**
- `tsvector` with `to_tsvector('simple', name)` — more precise but no fuzzy matching, worse for short identifiers
- Both — `pg_trgm` for symbol lookup + `tsvector` for content. We do exactly this: pg_trgm for symbols, tsvector for content.

### D3: Content Full-Text Search — tsvector

**Decision:** Use `tsvector` with GIN indexes for content section bodies.

**Rationale:**
- Content bodies are prose text (documentation, PDF sections, comments) — tsvector's tokenization is ideal
- `ts_rank()` and `ts_headline()` provide relevance scoring and snippets matching the current `ContentHit.Snippet`
- Content sections are write-once-read-rarely, so the insert cost of tsvector is acceptable
- Separating symbol search (pg_trgm) from content search (tsvector) mirrors the existing physical separation of `symbol_fts` and `content_fts` virtual tables

**SQL mapping:**

```sql
-- SQLite FTS5 (current)
CREATE VIRTUAL TABLE content_fts USING fts5(node_id, body);
SELECT node_id, body FROM content_fts WHERE content_fts MATCH ?;

-- PostgreSQL tsvector (new)
ALTER TABLE content_fts ADD COLUMN search_body tsvector
  GENERATED ALWAYS AS (to_tsvector('english', body)) STORED;
CREATE INDEX idx_content_fts_gin ON content_fts USING GIN (search_body);
SELECT node_id, ts_rank(search_body, query) AS score,
       ts_headline('english', body, query) AS snippet
FROM content_fts, plainto_tsquery('english', $1) AS query
WHERE search_body @@ query
ORDER BY score DESC
LIMIT $2;
```

### D4: Vector Search — pgvector with HNSW

**Decision:** Use `pgvector` extension with HNSW indexes.

**Rationale:**
- Current implementation is brute-force O(N): stream every vector BLOB from SQLite, decode, compute cosine distance, max-heap for top-k
- `pgvector` with HNSW gives approximate nearest neighbor with ~100x speedup on large corpora
- HNSW index build is O(N log N), queries are O(log N) — acceptable for the embedding pass cadence
- `vector_cosine_ops` maps directly to the existing cosine distance metric
- Available on all major platforms (RDS via `pgvector` extension, Supabase native, Neon native, self-hosted)

**SQL mapping:**

```sql
-- Create extension
CREATE EXTENSION vector;

-- Vector table (replacing current BLOB brute-force)
CREATE TABLE vectors (
    node_id TEXT PRIMARY KEY REFERENCES nodes(id),
    dims    INTEGER NOT NULL,
    vec     vector(384) NOT NULL
);

-- HNSW index for ANN
CREATE INDEX idx_vectors_hnsw ON vectors
  USING hnsw (vec vector_cosine_ops)
  WITH (m = 16, ef_construction = 200);

-- ANN query (replacing current Go brute-force)
SELECT node_id, vec <=> $1 AS distance
FROM vectors
ORDER BY vec <=> $1
LIMIT $2;
```

### D5: Table Design — WITHOUT ROWID → Standard PK Tables

**Decision:** Translate `WITHOUT ROWID` SQLite tables to standard PostgreSQL tables with PRIMARY KEY.

**Rationale:**
- PostgreSQL does not have `WITHOUT ROWID` — every table is a heap with optional PK index
- The semantic equivalent is `CREATE TABLE ... (PRIMARY KEY (...))` which creates a b-tree index on the PK
- All 8 sidecar tables (`file_mtimes`, `clone_shingles`, `constant_values`, `ref_facts`, `enrichment_state`, `repo_index_state`, `churn_enrichment`, etc.) translate directly
- PostgreSQL's index-only scans provide comparable read performance

**SQL mapping:**

```sql
-- SQLite WITHOUT ROWID
CREATE TABLE IF NOT EXISTS file_mtimes (
    repo_prefix TEXT NOT NULL,
    file_path   TEXT NOT NULL,
    mtime_ns    INTEGER NOT NULL,
    PRIMARY KEY (repo_prefix, file_path)
) WITHOUT ROWID;

-- PostgreSQL equivalent
CREATE TABLE IF NOT EXISTS file_mtimes (
    repo_prefix TEXT NOT NULL,
    file_path   TEXT NOT NULL,
    mtime_ns    BIGINT NOT NULL,
    PRIMARY KEY (repo_prefix, file_path)
);
```

### D6: Upsert Semantics — ON CONFLICT

**Decision:** Use `INSERT ... ON CONFLICT` instead of SQLite's `INSERT OR REPLACE` / `INSERT OR IGNORE`.

**Rationale:**
- SQLite `INSERT OR REPLACE` becomes `INSERT ... ON CONFLICT (id) DO UPDATE SET col1 = EXCLUDED.col1, ...`
- SQLite `INSERT OR IGNORE` becomes `INSERT ... ON CONFLICT ... DO NOTHING`
- PostgreSQL's `ON CONFLICT` is more explicit about which columns trigger the conflict and what gets updated

```sql
-- SQLite
INSERT OR REPLACE INTO nodes (id, kind, name, ...) VALUES (?, ?, ?, ...);

-- PostgreSQL
INSERT INTO nodes (id, kind, name, ...) VALUES ($1, $2, $3, ...)
ON CONFLICT (id) DO UPDATE SET
    kind = EXCLUDED.kind,
    name = EXCLUDED.name,
    ...;
```

### D7: Bulk Load — COPY FROM with UNLOGGED

**Decision:** Use `COPY FROM` into `UNLOGGED` tables for the bulk-load fast path, replacing the SQLite approach of dropping indexes + `synchronous=OFF`.

**Rationale:**
- SQLite's bulk path drops secondary indexes, sets `synchronous=OFF` on a pinned connection, then rebuilds indexes via `CREATE INDEX`
- PostgreSQL's equivalent: create `UNLOGGED` staging table, `COPY FROM` data in, `CREATE INDEX` on staging, `ALTER TABLE ... RENAME` to swap, `DROP` old table
- `UNLOGGED` tables skip WAL writes — risky on crash but acceptable for the indexer's cold-start phase (data can be re-indexed)
- For incremental writes (normal operation), standard `INSERT` with `synchronous_commit = OFF` is sufficient

```go
// Bulk load flow:
// 1. BEGIN;
// 2. SET synchronous_commit TO OFF;
// 3. CREATE UNLOGGED TABLE nodes_bulk (LIKE nodes INCLUDING ALL);
// 4. COPY nodes_bulk (id, kind, name, ...) FROM STDIN;
// 5. CREATE INDEX ON nodes_bulk (...);
// 6. ALTER TABLE nodes RENAME TO nodes_old;
// 7. ALTER TABLE nodes_bulk RENAME TO nodes;
// 8. DROP TABLE nodes_old;
// 9. COMMIT;
```

### D8: Schema Migrations — Inline Go (same strategy as SQLite)

**Decision:** Keep the schema versioning inline in Go, matching the existing SQLite approach, rather than pulling in `golang-migrate` or `goose`.

**Rationale:**
- The SQLite backend tracks schema version via `PRAGMA user_version` and applies DDL in `schema.go` with `IF NOT EXISTS`
- PostgreSQL equivalent: track version in a `schema_version` table, apply DDL idempotently with `IF NOT EXISTS`
- Adding `golang-migrate` introduces a file-system dependency (migration files on disk) that complicates the embedded binary
- Inline migrations keep the store package self-contained and testable
- If operational needs grow later, `golang-migrate` can be added as a separate migration path without removing inline versioning

```go
const schemaVersion = 1

var schemaMigrations = []struct {
    version int
    ddl     string
}{
    {1: `CREATE TABLE IF NOT EXISTS nodes (...); CREATE INDEX ...;`},
}

func (s *Store) ensureSchema() error {
    current := s.readSchemaVersion()
    for _, m := range schemaMigrations {
        if m.version > current {
            if _, err := s.pool.Exec(ctx, m.ddl); err != nil {
                return err
            }
            s.writeSchemaVersion(m.version)
        }
    }
    return nil
}
```

### D9: Connection Management — pgxpool with Dynamic Sizing

**Decision:** Use `pgxpool` with `MaxConns = runtime.NumCPU() * 2` (matching SQLite's `SetMaxOpenConns(runtime.NumCPU())` with headroom for background enrichment).

**Rationale:**
- SQLite uses `MaxOpenConns = runtime.NumCPU()` because WAL mode allows concurrent readers but writers serialize
- PostgreSQL benefits from more connections for parallel enrichment, resolver passes, and background analysis
- `NumCPU * 2` provides enough concurrency without overwhelming the PostgreSQL server
- Configurable via `--pg-pool-size` flag for production tuning
- Health check via `pgxpool.Config.MaxConnLifetime` (30 min) and `HealthCheckPeriod` (30 s)

### D10: Store Lock — Skip flock for PostgreSQL

**Decision:** Do NOT acquire a cross-process `flock` for PostgreSQL. The database server handles concurrent access internally.

**Rationale:**
- SQLite needs `store.sqlite.lock` because multiple processes writing to the same .sqlite file corrupts it
- PostgreSQL has built-in connection management, transaction isolation, and MVCC — no file lock needed
- The `storeLockHeld` gate in `shared_server.go` is extended: only acquire flock when `isEmbeddedBackend()` returns true (sqlite, bbolt — backends with a local file)

## Risks / Trade-offs

| Risk | Severity | Mitigation |
|---|---|---|
| **pgvector HNSW index build time** on large corpora (>500k vectors) | Medium | Build index after bulk load with `maintenance_work_mem` tuning. Can defer to background. |
| **pg_trgm GIN index size** vs SQLite FTS5 | Low | GIN indexes are larger than FTS5 inverted indexes (~2-3x). Acceptable given modern storage costs. |
| **Network latency** added to every store operation vs embedded SQLite | Medium | pgxpool keeps connections warm; prepared statements reduce round-trips. For latency-critical paths, the in-memory backend remains an option. |
| **PostgreSQL not available** in some environments (CI, offline) | Low | SQLite remains the default backend. Users who can't run PostgreSQL stay on SQLite. |
| **SQL dialect divergence** between SQLite and PostgreSQL | Low | The conformance suite (`storetest`) catches semantic differences. Shadow-test new queries against both backends during development. |
| **Connection leaks** under high concurrency | Medium | pgxpool with `MaxConnLifetime` and health checks prevents connection accumulation. Monitoring via pool stats exposed in daemon_health. |
| **Extension availability** (pgvector, pg_trgm) on managed Postgres | Low | pg_trgm is included in `pg_trgm` contrib (available everywhere). pgvector is available on RDS, Aurora, Supabase, Neon, and self-hosted. |
| **`UNLOGGED` table data loss** on crash during bulk load | Low | Cold start only — data re-indexed on restart. Incremental writes use logged tables. |

## Open Questions

1. **pgvector dimensions** — The current brute-force store doesn't enforce a fixed dimension. pgvector's HNSW index requires a fixed dimension at index creation time. The existing code uses 384d by default (all-MiniLM-L6-v2). Should we enforce this at the schema level?

2. **Prepared statements** — SQLite pre-compiles 30+ prepared statements at Open() time. pgx supports prepared statements but with different semantics (per-connection, auto-closed on pool return). Strategy: use pgx's `Prepare` on connection acquisition, or use a query cache?

3. **`gortex repos` command** — currently reads `store_sqlite.ReadRepoIndexStates(path)` directly. For PostgreSQL, should it connect to the database via DSN, or should we surface this information through the daemon's IPC socket instead?

4. **`gortex init`** — currently uses a temp SQLite store. Should it also support PostgreSQL init (connecting to a user-specified DSN), or keep using the temp SQLite as today?

5. **Connection string** — Should the `--pg-dsn` flag be the only connection mechanism, or should we also support `PGHOST`/`PGPORT`/`PGDATABASE`/`PGUSER`/`PGPASSWORD` environment variables (standard libpq convention)?
