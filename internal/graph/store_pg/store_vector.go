package store_pg

import (
	"fmt"

	"github.com/pgvector/pgvector-go"
	"github.com/zzet/gortex/internal/graph"
)

// This file implements graph.VectorSearcher on the PostgreSQL backend using
// the pgvector extension with HNSW indexes. It replaces the SQLite backend's
// in-memory brute-force O(N) cosine similarity search with true ANN queries.
//
// Design:
//   - Vectors are stored in the `vectors` table as pgvector's vector(384) type.
//   - BuildVectorIndex creates an HNSW index for ANN search.
//   - SimilarTo uses the `<=>` (cosine distance) operator with ORDER BY + LIMIT.
//   - GetEmbeddings returns raw vectors for post-rerank refinement.

// Compile-time assertion: *Store satisfies vector-search capability.
var _ graph.VectorSearcher = (*Store)(nil)

// BuildVectorIndex ensures the pgvector extension exists and creates the
// HNSW index for the given dimension. Idempotent.
func (s *Store) BuildVectorIndex(dims int) error {
	if s.refuseWrite("BuildVectorIndex") { return ErrReadOnlyStore }
	if dims <= 0 {
		return fmt.Errorf("store_pg: invalid vector dims: %d", dims)
	}

	// The extension and HNSW index are created in schema DDL, but ensure
	// index exists if not already present.
	_, err := s.pool.Exec(s.ctx,
		`CREATE INDEX IF NOT EXISTS idx_vectors_hnsw ON vectors USING hnsw (vec vector_cosine_ops) WITH (m = 16, ef_construction = 200)`)
	return err
}

// UpsertEmbedding inserts or replaces a single embedding vector.
func (s *Store) UpsertEmbedding(nodeID string, vec []float32) error {
	if s.refuseWrite("UpsertEmbedding") { return ErrReadOnlyStore }
	if nodeID == "" || len(vec) == 0 {
		return nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	pgvec := pgvector.NewVector(vec)
	_, err := s.pool.Exec(s.ctx,
		`INSERT INTO vectors (node_id, dims, vec) VALUES ($1, $2, $3)
		 ON CONFLICT (node_id) DO UPDATE SET dims = EXCLUDED.dims, vec = EXCLUDED.vec`,
		nodeID, len(vec), pgvec)
	return err
}

// BulkUpsertEmbeddings bulk-upserts vectors. Chunked to stay within
// parameter limits.
func (s *Store) BulkUpsertEmbeddings(items []graph.VectorItem) error {
	if s.refuseWrite("BulkUpsertEmbeddings") { return ErrReadOnlyStore }
	if len(items) == 0 {
		return nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	const vectorChunk = 300
	for i := 0; i < len(items); i += vectorChunk {
		end := minInt(i+vectorChunk, len(items))
		chunk := items[i:end]

		tx, err := s.pool.Begin(s.ctx)
		if err != nil {
			return err
		}

		for _, item := range chunk {
			if item.NodeID == "" || len(item.Vec) == 0 {
				continue
			}
			pgvec := pgvector.NewVector(item.Vec)
			if _, err := tx.Exec(s.ctx,
				`INSERT INTO vectors (node_id, dims, vec) VALUES ($1, $2, $3)
				 ON CONFLICT (node_id) DO UPDATE SET dims = EXCLUDED.dims, vec = EXCLUDED.vec`,
				item.NodeID, len(item.Vec), pgvec,
			); err != nil {
				_ = tx.Rollback(s.ctx)
				return err
			}
		}

		if err := tx.Commit(s.ctx); err != nil {
			return err
		}
	}
	return nil
}

// SimilarTo runs an ANN query: given a vector, return the k closest stored
// vectors ordered by ascending cosine distance.
func (s *Store) SimilarTo(vec []float32, limit int) ([]graph.VectorHit, error) {
	if len(vec) == 0 || limit <= 0 {
		return nil, nil
	}

	pgvec := pgvector.NewVector(vec)
	rows, err := s.pool.Query(s.ctx,
		`SELECT node_id, vec <=> $1 AS distance
		 FROM vectors
		 ORDER BY vec <=> $1
		 LIMIT $2`, pgvec, limit)
	if err != nil {
		return nil, fmt.Errorf("store_pg: similar to: %w", err)
	}
	defer rows.Close()

	var hits []graph.VectorHit
	for rows.Next() {
		var h graph.VectorHit
		if err := rows.Scan(&h.NodeID, &h.Distance); err != nil {
			return hits, err
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// GetEmbeddings reads back stored vectors for an explicit set of node IDs.
func (s *Store) GetEmbeddings(ids []string) map[string][]float32 {
	if len(ids) == 0 {
		return nil
	}

	uniq := dedupeNonEmpty(ids)
	out := make(map[string][]float32, len(uniq))

	for i := 0; i < len(uniq); i += lookupChunkSize {
		end := minInt(i+lookupChunkSize, len(uniq))
		chunk := uniq[i:end]

		rows, err := s.pool.Query(s.ctx,
			`SELECT node_id, vec FROM vectors WHERE node_id = ANY($1)`, chunk)
		if err != nil {
			return out
		}

		for rows.Next() {
			var nodeID string
			var pgvec pgvector.Vector
			if err := rows.Scan(&nodeID, &pgvec); err != nil {
				rows.Close()
				return out
			}
			out[nodeID] = pgvec.Slice()
		}
		rows.Close()
	}
	return out
}

