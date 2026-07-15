## ADDED Requirements

### Requirement: Bundle fingerprint invalidation
The PostgreSQL store SHALL correctly implement `SetBundleFingerprints` so that the daemon's analysis-pass fingerprints are propagated to the bundle cache, and stale entries are evicted when package fingerprints change. The `SearchSymbolBundles` method SHALL either consult the bundle cache before querying the database, or the bundle cache SHALL be removed entirely as dead code with a comment explaining why.

#### Scenario: SetBundleFingerprints propagates to cache
- **WHEN** `SetBundleFingerprints(fps)` is called with a non-empty fingerprint map
- **THEN** the store's bundle cache SHALL have its fingerprint map replaced with the new map

#### Scenario: Stale cache entries are evicted
- **WHEN** a package's fingerprint in a subsequent `SetBundleFingerprints` call differs from the stored one
- **THEN** the corresponding cache entry SHALL be removed

#### Scenario: SearchSymbolBundles uses cache
- **WHEN** `SearchSymbolBundles` is called for a query matching a cached package
- **THEN** the store SHALL return cached results instead of issuing database queries (if cache is consulted) OR if cache is intentionally disabled, the code SHALL be removed and documented

### Requirement: Per-instance bulk load state
The bulk-load state (`bulkState`) SHALL be a per-`Store` field, not a package-level variable. Multiple `Store` instances coexisting in the same process SHALL NOT share bulk-load state.

#### Scenario: Concurrent bulk loads on separate stores
- **WHEN** two `Store` instances simultaneously call `BeginBulkLoad` and `AddBatchBulk`
- **THEN** each store SHALL maintain independent node/edge buffers without data races

### Requirement: Content FTS handles non-UTF-8 input
The `AppendContent` method SHALL sanitize content bodies to valid UTF-8 before inserting into PostgreSQL, preventing encoding errors from crashing the indexing pipeline. The method SHALL use `strings.ToValidUTF8` or equivalent to replace or remove invalid byte sequences.

#### Scenario: Content with invalid UTF-8 bytes
- **WHEN** `AppendContent` receives a `ContentFTSItem` whose `Body` contains non-UTF-8 byte sequences (e.g., a bare `0xc2` byte)
- **THEN** the method SHALL sanitize the body to valid UTF-8 before insertion
- **THEN** the method SHALL NOT return an error for encoding reasons
- **THEN** the content index SHALL still contain the sanitized body

### Requirement: Correct SQL placeholder numbering in AppendContent
The `AppendContent` method SHALL generate correct `$N` parameter placeholders when items with empty `NodeID` are skipped. The offset SHALL be based on the count of actually-inserted items, not the loop index.

#### Scenario: AppendContent skips empty NodeID items
- **WHEN** `AppendContent` receives a chunk where the first item has an empty `NodeID` and the second item is valid
- **THEN** the generated SQL SHALL use `($1,$2,$3,$4,$5)` for the first valid item, not `($5,$6,$7,$8,$9)`
- **THEN** the insert SHALL succeed without parameter mismatch errors
