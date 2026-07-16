# Proposal: fix-follower-contract-registry

## Why

Same failure class as the fixed search readiness gate (`fix-follower-doc-body-search`): in-memory state derived at INDEX time is never hydrated from the store on a follower. Live v0.67.0 follower, store with 125 `contract` nodes across 20 repos: `contracts` (and the `api_impact` family) error with "no contract registry available — index a repository first", and the `contracts_orphans` inspection reports 0 violations — indistinguishable from a clean result, so it silently lies.

Root cause: `Server.effectiveContractRegistry()` resolves `multiIndexer.MergedContractRegistry()` / `indexer.ContractRegistry()`, both populated only by indexing runs. A follow-mode daemon never indexes, so the registry is empty even though every contract is persisted as graph nodes.

## What Changes

- Hydrate the contract registry from the store's persisted `contract` nodes when the daemon serves without indexing (follow mode / read-only), so `contracts`, `api_impact`, `change_contract` and the contract gates in inspections/review answer from the shared graph.
- Distinguish "registry genuinely empty" from "registry not built": inspections that depend on the registry MUST NOT report a clean 0 when the registry is unavailable — they surface an explicit unavailable/skipped marker instead.
- Fix the additional follower-honesty gaps found by the exhaustive live sweep (v0.67.0, ~110 tools exercised):
  - `workspace_info` still returns `{"mode":"unbound","repos":[]}` — it missed the unbound-enumeration fix that `list_repos` got; the two must agree.
  - `get_cfg` cannot anchor a valid node id on the follower ("path is not absolute and no indexed repo could anchor it") — build the CFG from the store blob, or mark it `follow_no_disk` if source-on-disk is genuinely required.
  - `sibling_diff_context` leaks a raw "could not resolve a repository root" instead of the `follow_no_disk` marker its sibling `diff_context` correctly returns.
  - `audit_agent_config` scans the (nonexistent) disk and answers "no agent config files found" (`files_scanned: 0`) — indistinguishable from a clean scan; needs the `follow_no_disk` marker or store-backed scanning.
  - `find_co_changing_symbols` answers "mining_in_progress — retry shortly" on a follower that never mines (first call only; later calls return plain empty) — the promise is false and the two responses are inconsistent.

## Capabilities

### New Capabilities

- `import-path-resolution`: ambiguous symbol names resolve to the nearest candidate (same package > same service > same repo), and existing imports are detected. Found live: `find_import_path(name=GortexManager, path=knowledge/core/kernel.py)` resolves to `workspace/core/gortex_manager.py::GortexManager` even though `knowledge/core/gortex_manager.py::GortexManager` sits in the caller's own package and is ALREADY imported by that file (`already_imported: false` is wrong on both counts).

### Modified Capabilities

- `follower-tool-honesty`: contract-backed tools on a follower answer from store-persisted contracts; registry-dependent inspections never report a silent clean result when the registry is absent.

## Impact

- `internal/mcp/server.go` `effectiveContractRegistry()` — store-backed fallback (build/merge a registry from `kind=contract` nodes, cached with invalidation on graph change).
- `internal/contracts` registry construction from persisted nodes.
- `internal/mcp/tools_enhancements.go` / `tools_api_impact.go` error paths; inspections runner (`contracts_orphans`).
- Verification: live probe — follower `contracts` returns the 125 stored contracts; `contracts_orphans` distinguishes skipped from clean.
