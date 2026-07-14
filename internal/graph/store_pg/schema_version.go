package store_pg

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

const currentSchemaVersion = 4

// schemaMigrationAdvisoryLockKey is the fixed key passed to
// pg_advisory_xact_lock around the migration loop so that concurrent
// Open calls serialize: the winner applies pending migrations while
// losers block, then re-read the version and no-op. The value is
// arbitrary but MUST remain stable across Gortex versions — it is
// documented in docs/pg-setup.md. (0x676F7274_6D696772 spells the
// ASCII "gortmigr".)
const schemaMigrationAdvisoryLockKey int64 = 0x676F72746D696772

type schemaMigration struct {
	version int
	ddl     string
}

var schemaMigrations = []schemaMigration{
	{version: 1, ddl: schemaSQL},
	{version: 2, ddl: `
-- Migration V2: vectors table dimension 384 → 50 to match the default
-- static (GloVe) embedder. Vectors are ephemeral (rebuilt each index
-- run via BulkUpsertEmbeddings), so dropping and recreating is clean.
DROP TABLE IF EXISTS vectors;
CREATE TABLE IF NOT EXISTS vectors (
    node_id TEXT PRIMARY KEY,
    dims    INTEGER NOT NULL,
    vec     vector(50) NOT NULL
);
`},
	{version: 3, ddl: `
-- Migration V3: convert the live nodes/edges tables to LOGGED. Earlier
-- versions of the destructive bulk-load swap left them UNLOGGED, which
-- means they are truncated on PG crash recovery and never shipped to
-- physical read replicas. SET LOGGED rewrites the table into the WAL
-- once; it is a no-op (does not rewrite) when the table is already
-- LOGGED, so this is safe to run on every upgrade path.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_class c
               JOIN pg_namespace n ON n.oid = c.relnamespace
               WHERE c.relname = 'nodes'
                 AND n.nspname = current_schema()
                 AND c.relpersistence = 'u') THEN
        EXECUTE 'ALTER TABLE nodes SET LOGGED';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_class c
               JOIN pg_namespace n ON n.oid = c.relnamespace
               WHERE c.relname = 'edges'
                 AND n.nspname = current_schema()
                 AND c.relpersistence = 'u') THEN
        EXECUTE 'ALTER TABLE edges SET LOGGED';
    END IF;
END $$;
`},
	{version: 4, ddl: `
-- Migration V4: content-addressed file blobs for diskless source reads
-- (follow-mode). Existing deployments gain the table empty; blobs
-- populate on the next writer index pass.
CREATE TABLE IF NOT EXISTS file_blobs (
    content_hash TEXT PRIMARY KEY,
    body         BYTEA NOT NULL,
    size         INTEGER NOT NULL
);
`},
}

// rowQuerier is satisfied by both *pgxpool.Pool and pgx.Tx, letting
// readSchemaVersion run against the pool during the initial probe and
// against the migration transaction after the advisory lock is held.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// readSchemaVersion returns the highest recorded schema version. A
// missing schema_version table (first boot against a blank database) is
// reported as version 0 with no error, so the migration loop bootstraps
// the schema. Existence is probed with to_regclass, which returns NULL
// rather than raising undefined_table — critical when this runs inside
// the migration transaction, where a raised error would abort the whole
// transaction (25P02). Every other query failure propagates so
// ensureSchema can fail Open instead of misreading a transient error as
// "blank database" and re-running DDL.
func (s *Store) readSchemaVersion(ctx context.Context, q rowQuerier) (int, error) {
	var reg *string
	if err := q.QueryRow(ctx, `SELECT to_regclass('schema_version')::text`).Scan(&reg); err != nil {
		return 0, err
	}
	if reg == nil {
		// Table does not exist: blank database, no schema yet.
		return 0, nil
	}
	var version int
	if err := q.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}

func writeSchemaVersion(ctx context.Context, tx pgx.Tx, version int) error {
	_, err := tx.Exec(ctx, `INSERT INTO schema_version (version) VALUES ($1) ON CONFLICT (version) DO NOTHING`, version)
	return err
}

func planSchemaMigration(stored, current int, migrations []schemaMigration) []schemaMigration {
	var toApply []schemaMigration
	for _, m := range migrations {
		if m.version > stored && m.version <= current {
			toApply = append(toApply, m)
		}
	}
	return toApply
}

// ErrSchemaVersionMismatch is the sentinel wrapped by
// SchemaVersionMismatchError; use errors.Is to detect the condition.
var ErrSchemaVersionMismatch = errors.New("store_pg: schema version mismatch")

// SchemaVersionMismatchError is returned from Open in read-only mode
// when the stored schema version differs from the version this binary
// expects. Read-only stores never migrate, so the operator must run a
// writer-mode process to reconcile the schema.
type SchemaVersionMismatchError struct {
	Stored   int
	Expected int
}

func (e *SchemaVersionMismatchError) Error() string {
	return fmt.Sprintf("store_pg: schema version mismatch: stored=%d expected=%d; "+
		"run a writer-mode gortex process against this database to migrate the schema before opening it read-only",
		e.Stored, e.Expected)
}

func (e *SchemaVersionMismatchError) Unwrap() error { return ErrSchemaVersionMismatch }

var errExtensionHint = errors.New(`store_pg: one or more required PostgreSQL extensions could not be created.
  Gortex needs pg_trgm and pgvector. Install them manually and restart:

    CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;
    CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;

  For platform-specific install guides see docs/pg-setup.md`)

func isExtensionError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "extension") ||
		strings.Contains(msg, `type "vector" does not exist`) ||
		strings.Contains(msg, "pg_trgm")
}

// ensureSchema reconciles the database schema with currentSchemaVersion.
//
// Read-only stores never execute DDL: they read the stored version and
// fail Open with a typed SchemaVersionMismatchError when it differs from
// what this binary expects.
//
// Writer stores run the migration loop inside pg_advisory_xact_lock so
// that concurrent Opens serialize. The lock winner applies pending
// migrations in a single transaction (DDL is transactional in
// PostgreSQL); losers block on the lock, re-read the version once they
// acquire it, and no-op when the schema is already current — so N
// simultaneous Opens against a blank database never race into duplicate
// CREATE statements (42P07).
func (s *Store) ensureSchema(ctx context.Context) error {
	stored, err := s.readSchemaVersion(ctx, s.pool)
	if err != nil {
		return fmt.Errorf("store_pg: read schema version: %w", err)
	}

	if s.config.ReadOnly {
		if stored != currentSchemaVersion {
			return &SchemaVersionMismatchError{Stored: stored, Expected: currentSchemaVersion}
		}
		return nil
	}

	if stored >= currentSchemaVersion {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store_pg: begin migration tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Serialize migrations across processes. The lock auto-releases when
	// the transaction commits or rolls back.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, schemaMigrationAdvisoryLockKey); err != nil {
		return fmt.Errorf("store_pg: acquire migration lock: %w", err)
	}

	// Re-read the version now that we hold the lock: a concurrent Open may
	// have migrated while we were blocked.
	stored, err = s.readSchemaVersion(ctx, tx)
	if err != nil {
		return fmt.Errorf("store_pg: re-read schema version under lock: %w", err)
	}
	if stored >= currentSchemaVersion {
		return tx.Commit(ctx)
	}

	toApply := planSchemaMigration(stored, currentSchemaVersion, schemaMigrations)
	for _, m := range toApply {
		if _, err := tx.Exec(ctx, m.ddl); err != nil {
			if isExtensionError(err) {
				return fmt.Errorf("%w\n  cause: %s", errExtensionHint, err.Error())
			}
			return fmt.Errorf("store_pg: apply schema version %d: %w", m.version, err)
		}
		if err := writeSchemaVersion(ctx, tx, m.version); err != nil {
			return fmt.Errorf("store_pg: record schema version %d: %w", m.version, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store_pg: commit migrations: %w", err)
	}
	return nil
}
