# code-source-blobs

## ADDED Requirements

### Requirement: The writer persists indexed file bytes as content-addressed blobs

Indexing on the PostgreSQL backend SHALL store every indexed file's bytes in a `file_blobs` table keyed by content hash (the same hash recorded in the `files` table), deduplicated: a hash already present SHALL NOT be rewritten. Blob writes SHALL ride the existing per-file index path and the bulk-load path (COPY). After a flush, blobs whose hash is no longer referenced by any `files` row SHALL be garbage-collected.

#### Scenario: Two repos share a file
- **WHEN** two indexed repos contain byte-identical files
- **THEN** `file_blobs` SHALL contain exactly one row for that content hash
- **AND** both `files` rows SHALL reference it

#### Scenario: Re-index updates the reference, GC prunes the orphan
- **WHEN** a file's content changes and the repo is re-indexed
- **THEN** the `files` row SHALL point at the new hash with the new blob stored
- **AND** the old blob SHALL be removed by garbage collection once unreferenced

### Requirement: The store serves file bytes by repo path

The PostgreSQL store SHALL expose a lookup returning the indexed bytes for a `(repo_prefix, file_path)` by joining `files.content_hash` to `file_blobs`, and a lookup by content hash. The returned bytes MUST be byte-identical to the file as indexed.

#### Scenario: Fetch source for an indexed file
- **WHEN** the blob lookup is called for an indexed code file
- **THEN** it SHALL return the exact bytes the writer indexed, with the content hash

### Requirement: Source reads fall back disk → store blob through one seam

The server's source-read helper SHALL resolve file bytes as: session overlay, then disk, then store blob. Every source-consuming read tool (`read_file`, `get_symbol_source`, `batch_symbols`, `smart_context` excerpts, `get_editing_context` reads, `compress_bodies`/salience transforms) SHALL inherit the fallback. Blob-served responses SHALL be marked `source: "store"` and use the content hash as etag. A file the graph knows but with no blob available SHALL produce the typed `follow_no_disk` error naming the writer re-run as the remedy.

#### Scenario: smart_context on a diskless follower
- **WHEN** `smart_context` runs on a follow-mode daemon for a task touching code symbols
- **THEN** the structural ranking AND the source excerpts SHALL both be served, the excerpts byte-exact from blobs

#### Scenario: get_symbol_source on code without disk
- **WHEN** `get_symbol_source` targets a function node on a follower
- **THEN** the response SHALL contain the symbol's exact source lines sliced from the blob
- **AND** carry `source: "store"` with the content-hash etag

#### Scenario: Graph knows the file, blob missing
- **WHEN** the schema was populated by a pre-blob writer and a source read targets a code file
- **THEN** the tool SHALL return the typed `follow_no_disk` error naming the missing blob and the writer re-run remedy

### Requirement: Text and structural search consume a FileSource abstraction

The trigram text search and the AST structural search SHALL read file bytes through a `FileSource` abstraction with disk-backed and blob-backed implementations, so `search_text` (literal/regex over code) and `search_ast` work on diskless followers against the indexed bytes.

#### Scenario: search_text regex on a follower
- **WHEN** `search_text` runs a regex query on a follow-mode daemon
- **THEN** the trigram index SHALL be built from blob bytes and matches SHALL reference the indexed content

#### Scenario: search_ast on a follower
- **WHEN** `search_ast` runs a structural query on a follow-mode daemon
- **THEN** files SHALL be parsed from blob bytes with results identical to a disk-backed run over the same content
