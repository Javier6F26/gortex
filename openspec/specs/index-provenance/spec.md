# index-provenance Specification

## Purpose
TBD - created by archiving change fix-follower-correctness-and-docs-search. Update Purpose after archive.
## Requirements
### Requirement: Per-repo sync provenance is queryable
The daemon SHALL record, per indexed repository/branch, the last-synced commit SHA and the timestamp of the sync that produced the current graph state, and SHALL expose both in `daemon status` output and in each `list_repos` entry.

#### Scenario: Client compares local HEAD with the index
- **WHEN** a client calls `list_repos` (or reads `daemon status`) after a repository has been indexed
- **THEN** each repository entry carries the last-synced commit SHA and sync timestamp, allowing the client to detect that its local HEAD is ahead of the index

#### Scenario: Provenance survives follow mode
- **WHEN** the same store is served by a follow-mode daemon
- **THEN** the provenance recorded by the writer is served unchanged to the follower's clients

