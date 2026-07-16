# follower-tool-honesty Specification

## Purpose
TBD - created by archiving change fix-follower-correctness-and-docs-search. Update Purpose after archive.
## Requirements
### Requirement: detect_changes errors without a working tree
On a daemon without a git working tree (follow mode / diskless), `detect_changes` SHALL fail with the `follow_no_disk:` error used by `review` and `review_pack`. It SHALL NOT return an empty changeset ("no indexed symbols affected", `risk: NONE`) that misrepresents the absence of a tree as the absence of changes.

#### Scenario: Follower rejects detect_changes
- **WHEN** `detect_changes` is called on a follow-mode daemon with no working tree
- **THEN** the call errors with a `follow_no_disk:` message naming the tool, and no changeset payload is returned

### Requirement: list_repos reports graph repos in unbound mode
When the daemon serves a multi-repo store without a bound workspace, `list_repos` SHALL enumerate the repository prefixes present in the graph (name and, when available, tracked branch) instead of returning `{"mode":"unbound","repos":[]}`.

#### Scenario: Follower lists indexed repos
- **WHEN** `list_repos` is called on a follow-mode daemon whose store contains repositories
- **THEN** the response lists each indexed repository prefix, and `mode` reflects the unbound/follower serving mode

### Requirement: Contract tools answer from persisted contracts on a follower
On a daemon that serves without indexing (follow mode / read-only store), contract-backed tools (`contracts`, `api_impact`, `change_contract`) SHALL resolve their registry from the contract nodes persisted in the graph store, not require an in-process indexing run. "Index a repository first" SHALL only be returned when the store truly holds no contract nodes.

#### Scenario: Follower lists stored contracts
- **WHEN** a follow-mode daemon serves a store containing `kind=contract` nodes and `contracts` is called
- **THEN** the persisted contracts are returned instead of "no contract registry available"

#### Scenario: Genuinely empty store still errors honestly
- **WHEN** the store holds zero contract nodes and `contracts` is called
- **THEN** the tool reports that no contracts are indexed

### Requirement: Registry-dependent inspections never report a silent clean
Inspections that depend on the contract registry (`contracts_orphans`) SHALL distinguish "no violations" from "registry unavailable": when the registry cannot be resolved, the inspection result SHALL carry an explicit skipped/unavailable marker rather than a zero-violation success.

#### Scenario: Unavailable registry is visible in results
- **WHEN** `run_inspections` includes `contracts_orphans` and the contract registry is unavailable
- **THEN** the inspection's result block is marked skipped/unavailable, not `total: 0` ok

### Requirement: workspace_info and list_repos agree in unbound mode
`workspace_info` SHALL report the same repository set as `list_repos` on the same daemon: when the store holds repositories in unbound/follow mode, `workspace_info` SHALL enumerate them instead of returning an empty `repos` array.

#### Scenario: Follower workspace_info lists graph repos
- **WHEN** a follow-mode daemon serves a store with indexed repositories and `workspace_info` is called
- **THEN** its `repos` array matches `list_repos` (names at minimum) rather than being empty

### Requirement: Every disk-dependent tool declares follow_no_disk uniformly
Every tool whose data source is the local working tree or filesystem SHALL fail on a diskless follower with the `follow_no_disk:` marker — never a raw internal error (`sibling_diff_context`: "could not resolve a repository root"), a clean-looking empty result (`audit_agent_config`: "no agent config files found"), or an unfulfillable promise (`find_co_changing_symbols`: "mining_in_progress — retry shortly" on a daemon that never mines).

#### Scenario: sibling_diff_context is guarded like diff_context
- **WHEN** `sibling_diff_context` is called on a follower without a working tree
- **THEN** it errors with a `follow_no_disk:` message naming the tool

#### Scenario: audit_agent_config does not fake a clean scan
- **WHEN** `audit_agent_config` is called on a diskless follower
- **THEN** the response is a `follow_no_disk:` error (or a store-backed scan), not `files_scanned: 0` with a clean message

#### Scenario: co-change mining status is truthful
- **WHEN** `find_co_changing_symbols` is called on a daemon that does not run co-change mining
- **THEN** the response states co-change data is unavailable on this daemon — it does not claim mining is in progress, and repeated calls answer consistently

### Requirement: Disk-writing tools are sealed on the follower
Tools that write to the local filesystem (`generate_wiki`, and any generator with a disk output) SHALL be rejected by the follower's write seal with an explicit error — the operating system's permission denial MUST never be the backstop that stops the write.

#### Scenario: generate_wiki is sealed, not crashed
- **WHEN** `generate_wiki` is called on a read-only follower
- **THEN** the response is a seal/`follow_no_disk` error naming the tool, not "mkdir wiki: permission denied"

### Requirement: get_cfg works from the store or is guarded
`get_cfg` SHALL build the control-flow graph from source text available in the graph store when no working tree exists; if disk source is genuinely required, it SHALL fail with the `follow_no_disk:` marker instead of a path-anchoring error for a valid node id.

#### Scenario: CFG for an indexed symbol on a follower
- **WHEN** `get_cfg` is called with a node id that `get_symbol_source` resolves on the same follower
- **THEN** it returns the CFG built from stored source, or a `follow_no_disk:` error — never "no indexed repo could anchor it"

### Requirement: Hydrated contract registry has node parity
The store-backed contract registry SHALL materialize one entry per persisted `contract` node: on a follower, `contracts {all_repos: true}` SHALL report the same total as the store's contract-node count, and per-repo listings SHALL sum to it.

#### Scenario: Follower contract count matches the store
- **WHEN** the store holds N `kind=contract` nodes and `contracts {all_repos: true}` is called on a follower
- **THEN** the response totals N contracts (not zero) and each carries its provider/consumer metadata

### Requirement: Inspection responses are internally consistent
`run_inspections` SHALL never report violations in `summary` that are absent from `results` without an explicit truncation marker: for every inspection, the violations returned plus a `truncated` flag MUST account for the summary count, at every `max_per_inspection` value.

#### Scenario: High cap does not drop the result block
- **WHEN** `run_inspections` is called with `max_per_inspection` greater than or equal to an inspection's violation count
- **THEN** the inspection's block appears in `results` with all its violations, and `summary.total_violations` matches what the blocks carry

### Requirement: Orphan detection runs against a populated registry
`contracts_orphans` SHALL match providers against consumers from the hydrated registry; a state where 100% of contracts are orphaned because the registry hydrated empty SHALL be impossible (parity requirement above) or reported as skipped/unavailable.

#### Scenario: Orphans reflect real matching
- **WHEN** the store holds providers with known consumers and `contracts_orphans` runs on a follower
- **THEN** matched pairs are not reported as orphans

