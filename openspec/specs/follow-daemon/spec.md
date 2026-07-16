# follow-daemon Specification

## Purpose
TBD - created by archiving change follow-mode. Update Purpose after archive.
## Requirements
### Requirement: Follow mode boots diskless and read-only

`gortex daemon start --follow` SHALL require `--backend postgres` (refusing other backends at startup with a clear error), open the graph store in read-only mode, construct the MCP server without a MultiIndexer, and publish readiness (`snapshot_loaded` → `ready`) without running any indexing, warmup reconcile, file watcher, reconcile janitor, enrichment, embedder, or LSP subprocess.

#### Scenario: Follower boots against a populated schema with no repos on disk
- **WHEN** a follow-mode daemon starts on a machine with no repo checkouts, pointed at a populated schema
- **THEN** it SHALL reach the `ready` phase without executing any store write
- **AND** graph read tools (`search_symbols`, `get_repo_outline`, `find_files`, traversal, `search_text` content fallback) SHALL serve the schema's data

#### Scenario: Non-postgres backend refused
- **WHEN** `--follow` is combined with the sqlite or memory backend
- **THEN** startup SHALL fail with an error naming the postgres requirement

#### Scenario: Freshness without reload
- **WHEN** the writer commits new rows for a repo while a follower is running
- **THEN** subsequent follower queries SHALL observe the new data without any restart or reload action

### Requirement: Follow mode seals the write surface at three layers

In follow mode: the tool preset SHALL be forced to `readonly` in `hide` mode (not widenable by `GORTEX_TOOLS` or config); the control-channel RPCs `track`, `untrack`, `reload`, and `reload-servers` SHALL return a typed `follow_mode` error; and the store SHALL be opened with the read-only guard so any residual write attempt is refused at the store layer. The residual writers that bypass presets SHALL be disabled: rationale graph projection, co-change boot prewarm, federation proxy hydration, and the `ensureFresh` read-path self-heal. `post_review` and `feedback` SHALL be denied.

#### Scenario: Mutating tool call
- **WHEN** a client calls `edit_file` or `index_repository` on a follower
- **THEN** the call SHALL be rejected by the preset gate with the tool-blocked error

#### Scenario: Control-channel track against a follower
- **WHEN** `gortex track <path>` targets a follow-mode daemon
- **THEN** the control RPC SHALL fail with a typed `follow_mode` error and no store write

#### Scenario: Residual writer paths are inert
- **WHEN** a client calls the `why` tool or memory tools on a follower
- **THEN** no graph-store write (rationale projection) SHALL occur
- **AND** machine-local sidecar writes MAY still occur

#### Scenario: Store guard as backstop
- **WHEN** any code path attempts a store mutation on a follower despite the upper gates
- **THEN** the read-only store SHALL refuse it and record the refusal in health state

### Requirement: Follow mode reports mode, lag, and health

`gortex daemon status` against a follower SHALL report `mode: follow` and a freshness lag derived from `repo_index_state.indexed_at`. When HTTP is enabled, `/healthz` SHALL report non-healthy when store health shows persistent degraded reads, and — when a maximum lag is configured — when the freshness lag exceeds it.

#### Scenario: Status on a follower
- **WHEN** `gortex daemon status` queries a follow-mode daemon
- **THEN** the output SHALL include the follow mode marker and the current freshness lag

#### Scenario: healthz with lag threshold
- **WHEN** `GORTEX_FOLLOW_MAX_LAG` is set and the schema's newest `indexed_at` is older than the threshold
- **THEN** `/healthz` SHALL return a non-200 status naming lag as the cause

### Requirement: Git-dependent tools fail typed in follow mode

Tools that require a git working tree or history (diff/review/PR tools, blame/churn/coverage/release enrichment mining) SHALL return a typed, recoverable error with condition `follow_no_disk` and a hint to run them against the writer or a local checkout, instead of a generic resolution error or a crash. Source reads on code files are served from blobs (see `code-source-blobs`) and only fail typed when the blob is genuinely absent.

#### Scenario: Diff review on a follower
- **WHEN** a diff/review tool is invoked on a follow-mode daemon
- **THEN** the response SHALL be a `follow_no_disk` typed error, not a panic or a bare path-resolution failure

