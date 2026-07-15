## ADDED Requirements

### Requirement: File mtime persistence
The PostgreSQL backend SHALL persist per-file modification times (`graph.FileMtimeWriter`, `graph.FileMtimeReader`, `graph.FileMtimeReplacer`, `graph.FileMtimeDeleter`) in a sidecar table.

#### Scenario: Bulk set stores per-repo mtimes
- **WHEN** the indexer records file mtimes after indexing
- **THEN** the backend upserts each (repo_prefix, file_path, mtime_ns) row

#### Scenario: Replace prunes deleted files
- **WHEN** a full reindex produces the authoritative mtime set
- **THEN** the backend replaces all mtime rows for the repo, removing entries for files no longer present

#### Scenario: Delete removes specific file mtimes
- **WHEN** a file is deleted and the incremental path needs to clean up
- **THEN** the backend removes the mtime rows for the specified files

#### Scenario: Load returns all mtimes for a repo
- **WHEN** the daemon warm-restarts and needs to reconcile mtimes
- **THEN** the backend returns all stored (file_path, mtime) pairs for the repo prefix

### Requirement: Ref facts persistence
The PostgreSQL backend SHALL persist per-file resolved-reference facts (`graph.RefFactsWriter`, `graph.RefFactsReader`) in a sidecar table.

#### Scenario: Bulk set stores per-file reference facts
- **WHEN** a file is indexed and its references are resolved
- **THEN** the backend upserts the per-reference facts keyed by (repo, from_id, to_id, kind, line)

#### Scenario: Read by file returns facts for specific files
- **WHEN** the audit/diff path requests reference facts for a set of source files
- **THEN** the backend returns all matching facts

#### Scenario: Read by target returns reverse lookup
- **WHEN** re-resolution needs to find files referencing changed symbols
- **THEN** the backend returns facts grouped by source file, keyed by target node IDs

### Requirement: Clone shingle persistence
The PostgreSQL backend SHALL persist per-symbol MinHash shingle sets (`graph.CloneShingleWriter`, `graph.CloneShingleReader`) in a sidecar table.

#### Scenario: Bulk set stores shingle sets per repo
- **WHEN** the clone-detection pass computes shingle sets
- **THEN** the backend persists the (node_id, shingle_blob) rows for the repo prefix

#### Scenario: Delete removes specific node shingles
- **WHEN** a symbol is evicted or rebuilt
- **THEN** the backend removes the shingle rows for the specified node IDs

#### Scenario: Load restores all shingles for a repo
- **WHEN** the daemon warm-restarts and needs to rebuild the CMS
- **THEN** the backend returns all persisted (node_id, shingles) pairs for the repo

### Requirement: Constant value persistence
The PostgreSQL backend SHALL persist KindConstant literal values (`graph.ConstantValueWriter`, `graph.ConstantValueReader`) in a sidecar table.

#### Scenario: Bulk set stores constant values per repo
- **WHEN** the indexer discovers string/numeric constant values
- **THEN** the backend upserts the (node_id, value) rows with file_path for scoped eviction

#### Scenario: Read by node IDs returns values
- **WHEN** the resolver needs to dereference a const identifier
- **THEN** the backend returns the literal values for the requested node IDs

### Requirement: Enrichment state persistence
The PostgreSQL backend SHALL persist per-(repo, provider) enrichment completion markers (`graph.EnrichmentStateStore`) in a sidecar table.

#### Scenario: Get returns stored enrichment state
- **WHEN** the deferred-enrichment gate checks if a provider has already enriched a repo
- **THEN** the backend returns the stored (indexed_sha, completed_at, coverage) row if present

#### Scenario: Set records enrichment completion
- **WHEN** a semantic enrichment pass finishes for a repo
- **THEN** the backend upserts the enrichment state row

### Requirement: File metadata persistence
The PostgreSQL backend SHALL persist per-file metadata (content hash, size, node count, parse errors) via `graph.FileMetaWriter` and `graph.FileMetaReader`.

#### Scenario: Set stores file metadata per repo
- **WHEN** the indexer finishes extracting nodes from a file
- **THEN** the backend upserts the (repo, path, content_hash, size, node_count, errors) row

### Requirement: Enrichment sidecars
The PostgreSQL backend SHALL persist git-churn, coverage, release, and blame enrichment data (`graph.ChurnEnrichmentWriter/Reader`, `graph.CoverageEnrichmentWriter/Reader`, `graph.ReleaseEnrichmentWriter/Reader`, `graph.BlameEnrichmentWriter/Reader`) in typed sidecar tables.

#### Scenario: Churn enrichment is persisted and readable
- **WHEN** a churn analysis pass computes per-node commit counts and age
- **THEN** the backend stores and can retrieve the churn rows per repo prefix

#### Scenario: Coverage enrichment is persisted and readable
- **WHEN** a coverage enrichment pass completes
- **THEN** the backend stores and can retrieve the coverage rows per repo

#### Scenario: Release enrichment is persisted
- **WHEN** git tag analysis finds the first release containing a file
- **THEN** the backend stores the (node_id, added_in) row

#### Scenario: Blame enrichment is persisted
- **WHEN** git blame enrichment runs
- **THEN** the backend stores the (node_id, commit_sha, email, timestamp) row
