## ADDED Requirements

### Requirement: ANN vector search via pgvector HNSW
The PostgreSQL backend SHALL provide approximate nearest-neighbor (ANN) vector search using the `pgvector` extension with HNSW indexes, replacing the current in-memory brute-force O(N) cosine similarity search.

#### Scenario: SimilarTo returns nearest vectors
- **WHEN** a query vector is provided
- **THEN** the backend returns the k closest stored vectors ordered by ascending cosine distance

#### Scenario: HNSW index accelerates queries
- **WHEN** BuildVectorIndex is called with a dimension count
- **THEN** the backend creates an HNSW index on the vectors table, and subsequent SimilarTo queries use it

### Requirement: Vector upsert and bulk upsert
The PostgreSQL backend SHALL support per-vector upsert and bulk upsert operations.

#### Scenario: Incremental vector update
- **WHEN** a file is reindexed and its embedding vector changes
- **THEN** the backend upserts the new vector for the node ID

#### Scenario: Bulk vector replacement
- **WHEN** a full embedding pass completes
- **THEN** the backend replaces all vectors for a repo prefix atomically

### Requirement: Vector dimension enforcement
The PostgreSQL backend SHALL enforce a fixed embedding dimension at the schema level.

#### Scenario: Mismatched dimension is rejected
- **WHEN** a vector with a different dimension than the declared index is inserted
- **THEN** the backend returns an error and the insert is rejected

### Requirement: Read-back embeddings by node ID
The PostgreSQL backend SHALL support reading stored vectors for explicit node IDs in one batch.

#### Scenario: GetEmbeddings returns raw vectors
- **WHEN** a post-rerank refinement stage requests vectors for specific node IDs
- **THEN** the backend returns the raw vectors for those IDs, with absent IDs omitted from the result map
