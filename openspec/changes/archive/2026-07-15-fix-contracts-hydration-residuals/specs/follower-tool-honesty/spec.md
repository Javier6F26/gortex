# follower-tool-honesty

## ADDED Requirements

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
