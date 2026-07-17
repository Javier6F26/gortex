# embedding-space-contract Specification

## Purpose
TBD - created by archiving change adaptive-embedding-dimensions. Update Purpose after archive.
## Requirements
### Requirement: Vector column dimension follows the active provider
The PostgreSQL vector store SHALL create the `vectors.vec` column with the dimensionality of the active embedding provider, discovered by probing the provider at daemon startup (embedding a sentinel input and measuring the returned vector length). The static schema SHALL NOT hardcode a dimension.

#### Scenario: Virgin store with OpenAI 1536-dim provider
- **WHEN** the daemon starts with `--embeddings-url https://api.openai.com/v1` and model `text-embedding-3-small` against an empty schema
- **THEN** the probe SHALL measure 1536 dimensions
- **THEN** `vectors.vec` SHALL be created as `vector(1536)` and the HNSW index built for 1536

#### Scenario: Bulk upserts match the column
- **WHEN** indexing persists embedding batches after initialization
- **THEN** no upsert SHALL fail with a dimension mismatch (SQLSTATE 22000)

### Requirement: Embedding-space metadata persisted and validated
The store SHALL persist the embedding space identity (provider, model, dimensions) on first initialization and SHALL validate the probed space against it on every writer startup. On mismatch the writer SHALL refuse vector operations with an error that names the stored space, the probed space, and the reset command — it SHALL NOT write vectors into a mismatched column nor migrate silently.

#### Scenario: Provider switched without reset
- **WHEN** the store was initialized with the in-process model (50 dims) and the daemon restarts configured for OpenAI (1536 dims)
- **THEN** startup SHALL fail fast with an error naming both spaces and the reset instruction
- **THEN** no vector writes SHALL be attempted

#### Scenario: Same space on restart
- **WHEN** the daemon restarts with the same provider and model recorded in the metadata
- **THEN** vector operations SHALL proceed without a reset

### Requirement: Explicit embedding-space reset
The CLI SHALL provide an explicit reset operation that drops the vector data and embedding-space metadata and reinitializes them for the currently configured provider. Structural graph data SHALL NOT be affected.

#### Scenario: Operator switches provider deliberately
- **WHEN** the operator runs the reset with the new provider configured
- **THEN** the `vectors` table SHALL be recreated with the new dimensionality and the metadata SHALL record the new space
- **THEN** structural nodes, edges and blobs SHALL remain intact

### Requirement: Optional dimension override
A dimension override (flag/env) SHALL, when set: replace the startup probe, size the vector column, and be forwarded as the requested dimensionality to providers that support it (OpenAI `dimensions` parameter). If the provider returns vectors whose length differs from the override, the write SHALL fail loudly rather than store truncated or padded vectors.

#### Scenario: Reduced-dimension OpenAI deployment
- **WHEN** the override is set to 512 with an OpenAI `text-embedding-3-*` model
- **THEN** embedding requests SHALL include `dimensions: 512` and the column SHALL be `vector(512)`

#### Scenario: Provider ignores the requested dimensionality
- **WHEN** the override is 512 but the provider returns 1536-dim vectors
- **THEN** the batch SHALL fail with an explicit dimension-contract error

### Requirement: Follower degrades to BM25 on embedding-space mismatch
A read-only follower SHALL read the stored `embedding_space` at boot and compare it against its own configured embedding provider/model/dimension. On mismatch the follower SHALL disable semantic vector search and serve those queries via BM25, logging a warning that names both spaces. The follower SHALL NOT surface a pgvector dimension error to callers, and SHALL NOT refuse to start.

#### Scenario: Follower configured for a different provider than the writer
- **WHEN** the shared schema's `embedding_space` records 1536-dim OpenAI but the follower is configured for the 50-dim in-process model
- **THEN** the follower SHALL start, log a warning naming both spaces, and answer search via BM25
- **THEN** no query SHALL fail with a pgvector dimension-mismatch error

#### Scenario: Follower matches the writer's space
- **WHEN** the follower's configured provider/model matches the stored `embedding_space`
- **THEN** semantic vector search SHALL be served normally

### Requirement: Legacy stores migrate metadata without reset
On upgrade, a store with an existing typed vector column and no embedding-space metadata SHALL synthesize the metadata from the column's declared dimension and the configured provider, so working deployments keep operating without an operator action.

#### Scenario: Healthy legacy deployment upgrades
- **WHEN** a store initialized as `vector(50)` under the in-process provider upgrades to this version with the same provider
- **THEN** metadata SHALL be synthesized (in-process, 50) and operation SHALL continue uninterrupted

