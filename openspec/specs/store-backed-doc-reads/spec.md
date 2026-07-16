# store-backed-doc-reads Specification

## Purpose
TBD - created by archiving change follow-mode. Update Purpose after archive.
## Requirements
### Requirement: read_file serves document files from the graph store when disk is unavailable

When the on-disk read fails (file absent) or the daemon runs in follow mode, `read_file` on a file whose graph nodes are document sections SHALL reconstruct the content from the store: markdown sections from each `KindDoc` node's `meta.section_text` ordered by start line; content-class sections (pdf/text/pptx/xlsx) from the content index bodies ordered by ordinal. The response SHALL be marked `source: "store"`, its etag SHALL derive from the reconstructed content (never a disk etag), and section-level (non-byte-exact) fidelity SHALL be documented in the response.

#### Scenario: Markdown vault file on a diskless follower
- **WHEN** `read_file` targets an indexed markdown file on a follow-mode daemon
- **THEN** the response SHALL contain the file's section texts concatenated in line order
- **AND** the response SHALL carry `source: "store"`

#### Scenario: Content-class document
- **WHEN** `read_file` targets an indexed pdf/text document whose section bodies live in the content index
- **THEN** the sections SHALL be fetched by ordinal via the store's content-by-file query and concatenated in order
- **AND** the response SHALL carry `source: "store"`

#### Scenario: Deleted file fallback on a normal daemon
- **WHEN** a document file was deleted from disk after indexing and `read_file` targets it on a normal (non-follow) daemon
- **THEN** the store-backed reconstruction SHALL be served with `source: "store"` instead of a read error

#### Scenario: Code file is not reconstructed
- **WHEN** `read_file` targets a code file with no disk available
- **THEN** the tool SHALL return the typed `follow_no_disk` error rather than a partial reconstruction

### Requirement: get_symbol_source serves doc nodes from their stored section text

`get_symbol_source` for a `KindDoc` node SHALL serve the node's own stored section text (exact for the node's line range) when disk is unavailable or in follow mode, marked `source: "store"`. Content-class nodes whose full body lives in the content index SHALL be served from it rather than from the 240-byte node snippet.

#### Scenario: Doc section node on a follower
- **WHEN** `get_symbol_source` targets a markdown section node on a follow-mode daemon
- **THEN** the response body SHALL equal the node's stored `section_text`
- **AND** the response SHALL carry `source: "store"`

#### Scenario: Content-class node body comes from the content index
- **WHEN** `get_symbol_source` targets a content-class node whose body exceeds the on-node snippet cap
- **THEN** the full body SHALL be fetched from the content index, not the truncated snippet

### Requirement: The store exposes an ordinal-ordered content-by-file query

The PostgreSQL content search capability SHALL expose a query returning all content bodies for a (repo prefix, file path) ordered by ordinal, sufficient to reconstruct a content-class document without disk access.

#### Scenario: Fetch content bodies for a file
- **WHEN** the content-by-file query is called for an indexed content-class document
- **THEN** it SHALL return every stored body row for that file ordered by ordinal

