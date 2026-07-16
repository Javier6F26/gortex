# Proposal: fix-contracts-hydration-residuals

## Why

v0.68.0 verification (live follower, store with 125 `contract` nodes) shows the contract-registry fix landed half-way, plus one response-shape bug in the inspections runner:

1. `contracts` no longer errors ("no contract registry available" is gone) but the store-hydrated registry is EMPTY: `contracts {all_repos: true}` → `total: 0`, per-repo → 0, while `graph_stats` reports 125 contract nodes and the `contracts_orphans` inspection reads all 125 from the same store. Two surfaces over one store disagree: the tool answers from a registry that hydrated no entries.
2. `run_inspections {inspections: contracts_orphans, max_per_inspection: 200}` returns `summary.total_violations: 125` with ZERO blocks in `results` — the violations vanish from the payload while the summary still counts them. At `max_per_inspection: 50` the block is present (`total: 50, truncated: true`).

Additionally, with the registry empty, all 125 contracts are flagged "provider with no counterpart" — orphan results cannot be trusted until matching runs against a populated registry.

## What Changes

- Fix the store-backed registry hydration so every persisted contract node materializes as a registry entry (field-parity test writer-vs-follower over the same store; `contracts` total on the follower equals the store's contract-node count).
- Fix the inspections runner so `results` blocks are never dropped while `summary` still counts their violations — whatever budget/cap logic removes the block must also reconcile the summary, and truncation is always marked.
- Re-baseline `contracts_orphans` on the populated registry and assert provider/consumer matching actually runs (not every contract orphan by definition).

## Capabilities

### New Capabilities

(none)

### Modified Capabilities

- `follower-tool-honesty`: the store-hydrated contract registry SHALL be complete (parity with persisted contract nodes), and inspection responses SHALL be internally consistent (summary counts match returned/officially-truncated blocks).

## Impact

- Store-backed registry construction added in v0.68.0 (`effectiveContractRegistry` fallback path) — hydration query/field mapping.
- Inspections runner response assembly (block vs summary reconciliation at high `max_per_inspection`).
- Verification: live probes recorded in the kmcp battery (`gortex068_verify.py` fix 1 tightened to assert count parity, plus a summary/blocks consistency probe).
