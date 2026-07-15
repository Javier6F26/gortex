## ADDED Requirements

### Requirement: Content body search via tsvector
The PostgreSQL backend SHALL provide full-text search over content section bodies (data_class="content") using the `tsvector` type with GIN indexes and relevance scoring.

#### Scenario: Prose query returns matching content sections with snippets
- **WHEN** a user searches for a phrase in content bodies
- **THEN** the backend returns matching content nodes ordered by ts_rank, each with a ts_headline snippet

#### Scenario: Per-repo content search scoping
- **WHEN** a user searches content scoped to a specific repo
- **THEN** the backend only searches sections belonging to that repo

#### Scenario: Empty content corpus returns no results
- **WHEN** no content sections have been indexed
- **THEN** any content search returns an empty result set

### Requirement: Content index lifecycle
The PostgreSQL backend SHALL support wipe, append, and build operations on the content search index.

#### Scenario: Wipe removes repo content
- **WHEN** a full reindex of a repo begins
- **THEN** the backend clears all content rows for that repo prefix

#### Scenario: Wipe file removes single file content
- **WHEN** an incremental reindex of a content file triggers
- **THEN** the backend clears only that file's content rows

#### Scenario: Append inserts content rows
- **WHEN** a content file is parsed and its sections are ready
- **THEN** the backend appends them to the content FTS index without wiping

#### Scenario: Content scan streams full bodies
- **WHEN** the content-to-code linker needs full section bodies
- **THEN** the backend streams every stored content row for a repo prefix, calling the consumer's callback with the full body
