package store_pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/zzet/gortex/internal/graph"
)

// This file implements the embedding-space contract (graph.VectorSpaceManager)
// on the PostgreSQL backend. The `vectors.vec` column dimension is NOT baked
// into the static schema — it must follow the active embedding provider, whose
// width is only known after a startup probe (or an operator override). The
// contract:
//
//   - EnsureVectorSpace binds the store to a discovered space: it creates the
//     vector column at the probed dimension on a virgin store, validates it on
//     every subsequent boot, and refuses (never silently migrates) on a
//     mismatch — the failure the adaptive-embedding-dimensions change exists to
//     prevent (a 1536-dim provider against a hardcoded vector(50) column made
//     every upsert fail with SQLSTATE 22000, silently losing the semantic
//     corpus).
//   - ReadEmbeddingSpace lets read-only followers detect a foreign space and
//     degrade semantic search to text search instead of erroring at query time.
//   - ResetVectorSpace is the explicit operator re-bind: drop vectors + space,
//     recreate for the new provider. Structural data is untouched.

// Compile-time assertion: *Store satisfies the embedding-space capability.
var _ graph.VectorSpaceManager = (*Store)(nil)

// resetCommandHint is the exact CLI invocation an operator runs to
// deliberately re-bind the store to a new embedding provider. Referenced in
// every mismatch error so the message is actionable.
const resetCommandHint = "gortex embeddings reset (with the new provider configured)"

// EnsureVectorSpace binds the vector store to want (the probed/overridden
// embedding space) and creates the vectors table sized for it. See the
// graph.VectorSpaceManager contract.
func (s *Store) EnsureVectorSpace(want graph.EmbeddingSpace) error {
	if s.refuseWrite("EnsureVectorSpace") {
		return ErrReadOnlyStore
	}
	if want.Dims <= 0 {
		return fmt.Errorf("store_pg: invalid embedding dims: %d", want.Dims)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	ctx := s.ctx

	stored, ok, err := s.readEmbeddingSpace(ctx)
	if err != nil {
		return fmt.Errorf("store_pg: read embedding space: %w", err)
	}
	if ok {
		// A space is on record. Validate and refuse on any divergence —
		// never write vectors into a column that describes a different space.
		if !stored.Compatible(want) {
			return &graph.EmbeddingSpaceMismatch{Stored: stored, Probed: want, ResetCmd: resetCommandHint}
		}
		// Compatible — make sure the column exists (defensive; it normally
		// does once a space is recorded).
		return s.createVectorsTable(ctx, want.Dims)
	}

	// No space on record. Distinguish a legacy store (typed vector column
	// already present from the pre-contract schema) from a virgin store.
	colDims, colExists, err := s.vectorColumnDims(ctx)
	if err != nil {
		return fmt.Errorf("store_pg: inspect vectors column: %w", err)
	}
	if colExists {
		// Legacy store: synthesize the space record from the column's declared
		// dimension + the configured provider, so a healthy deployment keeps
		// running without an operator action. If the live provider's width
		// disagrees with the column, this is the incident (a broken
		// deployment): fail fast with the reset instruction rather than
		// recording a space the column cannot hold.
		if colDims != want.Dims {
			return &graph.EmbeddingSpaceMismatch{
				Stored:   graph.EmbeddingSpace{Provider: want.Provider, Model: want.Model, Dims: colDims},
				Probed:   want,
				ResetCmd: resetCommandHint,
			}
		}
		synth := graph.EmbeddingSpace{Provider: want.Provider, Model: want.Model, Dims: colDims}
		return s.writeEmbeddingSpace(ctx, synth)
	}

	// Virgin store: create the column sized for the active provider and record
	// the space as the source of truth for every subsequent boot.
	if err := s.createVectorsTable(ctx, want.Dims); err != nil {
		return err
	}
	return s.writeEmbeddingSpace(ctx, want)
}

// ReadEmbeddingSpace returns the recorded embedding space, ok=false when none
// is recorded yet (or the table predates the contract). Safe on a read-only
// follower — a pure read, no write refusal.
func (s *Store) ReadEmbeddingSpace() (graph.EmbeddingSpace, bool, error) {
	return s.readEmbeddingSpace(s.ctx)
}

// ResetVectorSpace drops the vector data and space record and recreates the
// column for want. Structural tables are untouched. Refuses while another
// process holds the writer lock (the reset is meant to run against a stopped
// writer).
func (s *Store) ResetVectorSpace(want graph.EmbeddingSpace) error {
	if s.refuseWrite("ResetVectorSpace") {
		return ErrReadOnlyStore
	}
	if want.Dims <= 0 {
		return fmt.Errorf("store_pg: invalid embedding dims: %d", want.Dims)
	}

	// Refuse while another writer holds the schema lock. Acquire it here (if we
	// do not already hold it) so a running daemon's reset is rejected with a
	// clear conflict instead of racing its indexing.
	if s.lockConn == nil {
		if err := s.AcquireWriterLock(s.ctx); err != nil {
			return fmt.Errorf("store_pg: reset refused — %w (stop the writer daemon first)", err)
		}
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	ctx := s.ctx

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DROP TABLE IF EXISTS vectors`); err != nil {
		return fmt.Errorf("store_pg: drop vectors: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM embedding_space`); err != nil {
		return fmt.Errorf("store_pg: clear embedding_space: %w", err)
	}
	if _, err := tx.Exec(ctx, createVectorsDDL(want.Dims)); err != nil {
		return fmt.Errorf("store_pg: recreate vectors(%d): %w", want.Dims, err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO embedding_space (id, provider, model, dims) VALUES (1, $1, $2, $3)`,
		want.Provider, want.Model, want.Dims); err != nil {
		return fmt.Errorf("store_pg: record embedding_space: %w", err)
	}
	return tx.Commit(ctx)
}

// readEmbeddingSpace reads the single embedding_space row. A missing row or a
// missing table (a store below the contract's schema version) both report
// ok=false with no error, so callers treat "no space yet" uniformly.
func (s *Store) readEmbeddingSpace(ctx context.Context) (graph.EmbeddingSpace, bool, error) {
	var sp graph.EmbeddingSpace
	err := s.pool.QueryRow(ctx,
		`SELECT provider, model, dims FROM embedding_space WHERE id = 1`).
		Scan(&sp.Provider, &sp.Model, &sp.Dims)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || isUndefinedTable(err) {
			return graph.EmbeddingSpace{}, false, nil
		}
		return graph.EmbeddingSpace{}, false, err
	}
	return sp, true, nil
}

// vectorColumnDims reports the declared dimension of vectors.vec, and whether
// the vectors table exists at all. For pgvector's vector type the column's
// atttypmod IS the dimension (no varchar-style -4 offset); an untyped `vector`
// column reports atttypmod -1, surfaced here as dims 0.
func (s *Store) vectorColumnDims(ctx context.Context) (dims int, exists bool, err error) {
	var typmod int
	err = s.pool.QueryRow(ctx, `
		SELECT a.atttypmod
		FROM pg_attribute a
		WHERE a.attrelid = to_regclass('vectors')
		  AND a.attname = 'vec'
		  AND NOT a.attisdropped`).Scan(&typmod)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if typmod < 0 {
		typmod = 0
	}
	return typmod, true, nil
}

// createVectorsTable creates the vectors table sized for dims if it does not
// already exist. dims is caller-controlled (probe/override, validated > 0), so
// interpolating it into the DDL is safe — pgvector's vector(N) type takes the
// dimension as a type modifier, not a bindable parameter.
func (s *Store) createVectorsTable(ctx context.Context, dims int) error {
	if _, err := s.pool.Exec(ctx, createVectorsDDL(dims)); err != nil {
		return fmt.Errorf("store_pg: create vectors(%d): %w", dims, err)
	}
	return nil
}

// createVectorsDDL renders the CREATE TABLE for the vectors table at a given
// dimension. Centralized so EnsureVectorSpace and ResetVectorSpace stay in sync.
func createVectorsDDL(dims int) string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS vectors (
    node_id TEXT PRIMARY KEY,
    dims    INTEGER NOT NULL,
    vec     vector(%d) NOT NULL
)`, dims)
}

// writeEmbeddingSpace records the space as the single embedding_space row,
// leaving an existing row untouched (init paths never overwrite — ResetVectorSpace
// is the only mutation, and it clears the row first).
func (s *Store) writeEmbeddingSpace(ctx context.Context, sp graph.EmbeddingSpace) error {
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO embedding_space (id, provider, model, dims) VALUES (1, $1, $2, $3)
		 ON CONFLICT (id) DO NOTHING`,
		sp.Provider, sp.Model, sp.Dims); err != nil {
		return fmt.Errorf("store_pg: record embedding_space: %w", err)
	}
	return nil
}

// isUndefinedTable reports whether err is PostgreSQL's undefined_table (42P01),
// used to treat a missing embedding_space table (a pre-contract schema) as
// "no space recorded" rather than a hard error.
func isUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "42P01"
	}
	return false
}
