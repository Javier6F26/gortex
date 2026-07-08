package store_pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const currentSchemaVersion = 1

type schemaMigration struct {
	version int
	ddl     string
}

var schemaMigrations = []schemaMigration{
	{version: 1, ddl: schemaSQL},
}

func (s *Store) readSchemaVersion(ctx context.Context) (int, error) {
	var version int
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version)
	if err != nil {
		return 0, nil
	}
	return version, nil
}

func (s *Store) writeSchemaVersion(ctx context.Context, version int) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO schema_version (version) VALUES ($1) ON CONFLICT (version) DO NOTHING`, version)
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

func (s *Store) ensureSchema(ctx context.Context) error {
	stored, err := s.readSchemaVersion(ctx)
	if err != nil {
		return fmt.Errorf("store_pg: read schema version: %w", err)
	}
	if stored >= currentSchemaVersion {
		return nil
	}
	toApply := planSchemaMigration(stored, currentSchemaVersion, schemaMigrations)
	for _, m := range toApply {
		if _, err := s.pool.Exec(ctx, m.ddl); err != nil {
			if isExtensionError(err) {
				return fmt.Errorf("%w\n  cause: %s", errExtensionHint, err.Error())
			}
			return fmt.Errorf("store_pg: apply schema version %d: %w", m.version, err)
		}
		if err := s.writeSchemaVersion(ctx, m.version); err != nil {
			return fmt.Errorf("store_pg: record schema version %d: %w", m.version, err)
		}
	}
	return nil
}
