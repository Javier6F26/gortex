package store_pg

// This file contains the DDL for the PostgreSQL graph store. All
// statements are idempotent (IF NOT EXISTS) so they run cleanly against
// a fresh database and against an existing one.
//
// Translation notes from store_sqlite/schema.go:
//
//   - WITHOUT ROWID tables become standard heap tables with PRIMARY KEY.
//     PostgreSQL has no WITHOUT ROWID equivalent; the PK constraint creates
//     a b-tree index that provides comparable read performance.
//
//   - Meta maps use JSONB instead of BLOB so they are queryable through
//     standard PostgreSQL JSON operators. The Go-level API (Meta map)
//     is unchanged — pgx handles JSONB marshaling transparently.
//
//   - Symbol FTS uses pg_trgm GIN index on nodes.name instead of a
//     virtual FTS5 table. pg_trgm provides trigram similarity matching
//     with typo tolerance.
//
//   - Content FTS uses a generated tsvector column with a GIN index.
//     ts_rank and ts_headline provide relevance scoring and snippets.
//
//   - Vectors use the pgvector extension's vector type with HNSW index.
//
//   - BLOB columns for binary data (shingles, vectors) use BYTEA.
//
//   - INTEGER in SQLite maps to INTEGER or BIGINT. SQLite INTEGER can
//     hold up to 64-bit signed values, so BIGINT is the safe equivalent.

const schemaSQL = `
-- Gortex auto-installs these extensions on first connection. On managed
-- platforms (RDS, Supabase, Neon) that restrict CREATE EXTENSION
-- permissions, install them manually before starting Gortex:
--   CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;
--   CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;
CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;
CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;

-- Core tables
-- ===========

-- nodes is the primary symbol table. Meta is stored as JSONB for
-- queryability via PostgreSQL JSON operators. The id column is the
-- primary key; INSERT with ON CONFLICT DO UPDATE provides idempotent
-- upsert semantics matching the in-memory store.
CREATE TABLE IF NOT EXISTS nodes (
    id            TEXT PRIMARY KEY,
    kind          TEXT NOT NULL,
    name          TEXT NOT NULL,
    qual_name     TEXT NOT NULL DEFAULT '',
    file_path     TEXT NOT NULL,
    start_line    INTEGER NOT NULL DEFAULT 0,
    end_line      INTEGER NOT NULL DEFAULT 0,
    start_column  INTEGER NOT NULL DEFAULT 0,
    end_column    INTEGER NOT NULL DEFAULT 0,
    language      TEXT NOT NULL DEFAULT '',
    repo_prefix   TEXT NOT NULL DEFAULT '',
    workspace_id  TEXT NOT NULL DEFAULT '',
    project_id    TEXT NOT NULL DEFAULT '',
    signature     TEXT,
    visibility    TEXT,
    doc           TEXT,
    external      INTEGER,
    return_type   TEXT,
    is_async      INTEGER,
    is_static     INTEGER,
    is_abstract   INTEGER,
    is_exported   INTEGER,
    updated_at    BIGINT,
    data_class    TEXT,
    meta          JSONB
);

-- edges stores directed relationships between nodes. The UNIQUE constraint
-- on (from_id, to_id, kind, file_path, line) provides the same dedup
-- semantics as the SQLite version (INSERT OR IGNORE / ON CONFLICT DO NOTHING).
CREATE TABLE IF NOT EXISTS edges (
    id               BIGSERIAL,
    from_id          TEXT NOT NULL,
    to_id            TEXT NOT NULL,
    kind             TEXT NOT NULL,
    file_path        TEXT NOT NULL DEFAULT '',
    line             INTEGER NOT NULL DEFAULT 0,
    confidence       REAL NOT NULL DEFAULT 1.0,
    confidence_label TEXT NOT NULL DEFAULT '',
    origin           TEXT NOT NULL DEFAULT '',
    tier             TEXT NOT NULL DEFAULT '',
    cross_repo       BOOLEAN NOT NULL DEFAULT FALSE,
    meta             JSONB,
    UNIQUE(from_id, to_id, kind, file_path, line)
);

-- Sidecar tables
-- ==============

-- file_mtimes records per-file modification times for incremental reindex.
CREATE TABLE IF NOT EXISTS file_mtimes (
    repo_prefix TEXT NOT NULL,
    file_path   TEXT NOT NULL,
    mtime_ns    BIGINT NOT NULL,
    PRIMARY KEY (repo_prefix, file_path)
);

-- repo_index_state records per-repo freshness provenance.
CREATE TABLE IF NOT EXISTS repo_index_state (
    repo_prefix        TEXT PRIMARY KEY,
    indexed_sha        TEXT NOT NULL DEFAULT '',
    dirty              INTEGER NOT NULL DEFAULT 0,
    indexed_at         BIGINT NOT NULL DEFAULT 0,
    workspace_fp       TEXT NOT NULL DEFAULT '',
    node_count         INTEGER NOT NULL DEFAULT 0,
    edge_count         INTEGER NOT NULL DEFAULT 0,
    extractor_versions TEXT NOT NULL DEFAULT ''
);

-- enrichment_state records per-(repo, provider) enrichment completion.
CREATE TABLE IF NOT EXISTS enrichment_state (
    repo_prefix  TEXT NOT NULL,
    provider     TEXT NOT NULL,
    indexed_sha  TEXT NOT NULL DEFAULT '',
    completed_at BIGINT NOT NULL DEFAULT 0,
    coverage     REAL NOT NULL DEFAULT 0,
    PRIMARY KEY (repo_prefix, provider)
);

-- clone_shingles stores per-symbol MinHash shingle sets as BYTEA.
CREATE TABLE IF NOT EXISTS clone_shingles (
    node_id     TEXT PRIMARY KEY,
    repo_prefix TEXT NOT NULL DEFAULT '',
    shingles    BYTEA
);

-- constant_values stores per-KindConstant literal values.
CREATE TABLE IF NOT EXISTS constant_values (
    node_id     TEXT PRIMARY KEY,
    repo_prefix TEXT NOT NULL DEFAULT '',
    file_path   TEXT NOT NULL DEFAULT '',
    value       TEXT NOT NULL DEFAULT ''
);

-- files stores per-file metadata (content hash, size, node count, errors).
CREATE TABLE IF NOT EXISTS files (
    repo_prefix  TEXT NOT NULL DEFAULT '',
    file_path    TEXT NOT NULL,
    content_hash TEXT NOT NULL DEFAULT '',
    size         INTEGER NOT NULL DEFAULT 0,
    node_count   INTEGER NOT NULL DEFAULT 0,
    errors       TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (repo_prefix, file_path)
);

-- ref_facts stores per-file resolved-reference facts.
CREATE TABLE IF NOT EXISTS ref_facts (
    repo_prefix TEXT NOT NULL DEFAULT '',
    from_id     TEXT NOT NULL,
    to_id       TEXT NOT NULL,
    kind        TEXT NOT NULL,
    ref_name    TEXT NOT NULL DEFAULT '',
    line        INTEGER NOT NULL DEFAULT 0,
    origin      TEXT NOT NULL DEFAULT '',
    tier        TEXT NOT NULL DEFAULT '',
    candidates  TEXT NOT NULL DEFAULT '',
    file_path   TEXT NOT NULL DEFAULT '',
    lang        TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (repo_prefix, from_id, to_id, kind, line)
);

-- vectors stores symbol embedding vectors using pgvector's vector type.
CREATE TABLE IF NOT EXISTS vectors (
    node_id TEXT PRIMARY KEY,
    dims    INTEGER NOT NULL,
    vec     vector(50) NOT NULL
);

-- churn_enrichment stores git-churn data per node.
CREATE TABLE IF NOT EXISTS churn_enrichment (
    node_id        TEXT PRIMARY KEY,
    repo_prefix    TEXT NOT NULL DEFAULT '',
    commit_count   INTEGER NOT NULL DEFAULT 0,
    age_days       INTEGER NOT NULL DEFAULT 0,
    churn_rate     REAL NOT NULL DEFAULT 0,
    last_author    TEXT NOT NULL DEFAULT '',
    last_commit_at TEXT NOT NULL DEFAULT '',
    head_sha       TEXT NOT NULL DEFAULT '',
    branch         TEXT NOT NULL DEFAULT '',
    computed_at    TEXT NOT NULL DEFAULT ''
);

-- coverage_enrichment stores code coverage data per node.
CREATE TABLE IF NOT EXISTS coverage_enrichment (
    node_id      TEXT PRIMARY KEY,
    repo_prefix  TEXT NOT NULL DEFAULT '',
    coverage_pct REAL NOT NULL DEFAULT 0,
    num_stmt     INTEGER NOT NULL DEFAULT 0,
    hit          INTEGER NOT NULL DEFAULT 0
);

-- release_enrichment stores per-file "added_in <tag>" data.
CREATE TABLE IF NOT EXISTS release_enrichment (
    node_id     TEXT PRIMARY KEY,
    repo_prefix TEXT NOT NULL DEFAULT '',
    added_in    TEXT NOT NULL DEFAULT ''
);

-- blame_enrichment stores per-symbol latest-author data.
CREATE TABLE IF NOT EXISTS blame_enrichment (
    node_id     TEXT PRIMARY KEY,
    repo_prefix TEXT NOT NULL DEFAULT '',
    commit_sha  TEXT NOT NULL DEFAULT '',
    email       TEXT NOT NULL DEFAULT '',
    ts          BIGINT NOT NULL DEFAULT 0
);

-- Indexes
-- =======

-- Node indexes (mirroring SQLite secondary indexes)
CREATE INDEX IF NOT EXISTS idx_nodes_name ON nodes(name);
CREATE INDEX IF NOT EXISTS idx_nodes_kind ON nodes(kind);
CREATE INDEX IF NOT EXISTS idx_nodes_file ON nodes(file_path);
CREATE INDEX IF NOT EXISTS idx_nodes_repo_prefix ON nodes(repo_prefix) WHERE repo_prefix <> '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_nodes_qual_name ON nodes(qual_name) WHERE qual_name <> '';

-- Edge indexes
CREATE INDEX IF NOT EXISTS idx_edges_from_id ON edges(from_id, kind);
CREATE INDEX IF NOT EXISTS idx_edges_to_id ON edges(to_id);
CREATE INDEX IF NOT EXISTS idx_edges_kind ON edges(kind);
CREATE INDEX IF NOT EXISTS idx_edges_repo_prefix ON edges(from_id) WHERE from_id LIKE '%::%';

-- Sidecar indexes
CREATE INDEX IF NOT EXISTS idx_constant_values_by_file ON constant_values(repo_prefix, file_path);
CREATE INDEX IF NOT EXISTS idx_ref_facts_by_file ON ref_facts(repo_prefix, file_path);
CREATE INDEX IF NOT EXISTS idx_ref_facts_by_target ON ref_facts(repo_prefix, to_id);
CREATE INDEX IF NOT EXISTS idx_files_with_errors ON files(repo_prefix) WHERE errors <> '';

-- Enrichment sidecar indexes
CREATE INDEX IF NOT EXISTS idx_churn_by_repo ON churn_enrichment(repo_prefix) WHERE repo_prefix <> '';
CREATE INDEX IF NOT EXISTS idx_coverage_by_repo ON coverage_enrichment(repo_prefix) WHERE repo_prefix <> '';
CREATE INDEX IF NOT EXISTS idx_release_by_repo ON release_enrichment(repo_prefix) WHERE repo_prefix <> '';
CREATE INDEX IF NOT EXISTS idx_blame_by_repo ON blame_enrichment(repo_prefix) WHERE repo_prefix <> '';

-- Full-text search indexes
-- ========================

-- Symbol FTS: pg_trgm GIN index on nodes.name for trigram similarity search.
-- This replaces SQLite's FTS5 virtual table for symbol names.
-- The index supports:
--   WHERE name % 'query'      (trigram similarity)
--   WHERE name ILIKE '%q%'    (case-insensitive substring, fallback)
CREATE INDEX IF NOT EXISTS idx_nodes_name_trgm ON nodes USING GIN (name gin_trgm_ops);

-- Content FTS: generated tsvector column with GIN index.
-- This replaces SQLite's content_fts FTS5 virtual table for content bodies.
-- search_body is a generated tsvector column for full-text search.
CREATE TABLE IF NOT EXISTS content_fts (
    node_id     TEXT NOT NULL,
    repo_prefix TEXT NOT NULL DEFAULT '',
    file_path   TEXT NOT NULL DEFAULT '',
    ordinal     INTEGER NOT NULL DEFAULT 0,
    body        TEXT NOT NULL,
    search_body tsvector GENERATED ALWAYS AS (to_tsvector('english', body)) STORED
);

CREATE INDEX IF NOT EXISTS idx_content_fts_gin ON content_fts USING GIN (search_body);
CREATE INDEX IF NOT EXISTS idx_content_fts_repo ON content_fts(repo_prefix);
CREATE INDEX IF NOT EXISTS idx_content_fts_file ON content_fts(file_path);

-- file_blobs stores the exact bytes of every indexed file, keyed by the
-- content hash the files table already records. Content addressing
-- deduplicates identical files across repos and re-indexes. body is BYTEA;
-- PostgreSQL TOAST transparently compresses and out-of-lines large values,
-- so source text (highly compressible) costs far less than its raw size.
-- Diskless followers slice source out of here (see code-source-blobs).
CREATE TABLE IF NOT EXISTS file_blobs (
    content_hash TEXT PRIMARY KEY,
    body         BYTEA NOT NULL,
    size         INTEGER NOT NULL
);

-- Schema version tracking table.
CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
`
