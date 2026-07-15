## ADDED Requirements

### Requirement: Symbol name search via pg_trgm
The PostgreSQL backend SHALL provide full-text search over symbol names using the `pg_trgm` extension with GIN indexes and trigram similarity scoring.

#### Scenario: Exact name match returns the symbol with highest score
- **WHEN** a user searches for an exact symbol name (e.g., "parseFile")
- **THEN** the backend returns that symbol as the first result with a similarity score of 1.0

#### Scenario: Fuzzy name match returns similar symbols
- **WHEN** a user searches for a symbol name with a typo (e.g., "parsFile")
- **THEN** the backend returns symbols whose name has high trigram similarity to the query, ordered by descending score

#### Scenario: Unindexed prefix matches return results
- **WHEN** a user searches for a substring prefix
- **THEN** the backend returns matching symbols via pg_trgm's word similarity

#### Scenario: No matching symbols returns empty result
- **WHEN** a user searches for a term that matches no symbol names
- **THEN** the backend returns an empty result set

### Requirement: Symbol bundle search
The PostgreSQL backend SHALL support batched symbol search that returns each hit's node, score, and pre-fetched in/out edges for reranking.

#### Scenario: Search bundles include edge adjacency
- **WHEN** the rerank pipeline requests symbol bundles for a query
- **THEN** each result includes the node, its BM25-equivalent score, and its in/out edges in a single round-trip

### Requirement: FTS index lifecycle
The PostgreSQL backend SHALL support upsert, bulk upsert, index build, and index wipe operations on the symbol search index.

#### Scenario: Incremental update inserts a new symbol
- **WHEN** a file is reindexed and a new symbol is added
- **THEN** the backend upserts the symbol's name into the pg_trgm index

#### Scenario: Bulk upsert replaces per-repo corpus
- **WHEN** a full repo reindex completes
- **THEN** the backend replaces all symbol rows for that repo prefix atomically

#### Scenario: Index build is idempotent
- **WHEN** BuildSymbolIndex is called multiple times on the same data
- **THEN** the index is not duplicated or corrupted
