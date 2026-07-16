# Tasks: fix-follower-contract-registry

## 1. Store-backed contract registry

- [x] 1.1 Build a `contracts.Registry` from persisted `kind=contract` nodes; verify field parity writer-vs-follower on the same store in a test
- [x] 1.2 Wire it as the fallback in `effectiveContractRegistry()` with caching + invalidation on graph change
- [x] 1.3 Follow-mode integration test: `contracts` returns stored contracts through a postgres follower; empty store still errors honestly

## 2. Inspections honesty

- [x] 2.1 `contracts_orphans` (and any registry-dependent inspection) reports skipped/unavailable instead of a silent `total: 0` when the registry is absent

## 3. Audit the failure class

- [x] 3.1 Sweep MCP handlers for other index-time in-memory state consulted on the follower (guards, agent/notebook registries, scopes); table each as works-from-store / fixed / documented-N/A — see design.md "Audit (Decision 2 / task 3.1)"
- [ ] 3.2 Re-run the kmcp probe battery against the deployed follower after release; add probes for contracts, workspace_info, get_cfg, sibling_diff_context, audit_agent_config and find_co_changing_symbols — OPERATIONAL: blocked on redeploying the follower with this change; the in-repo follower integration tests (follow_integration_test.go) cover the same tools ahead of the live re-run

## 4. Sweep findings (live v0.67.0, ~110 tools exercised)

- [x] 4.1 `workspace_info`: apply the same unbound-enumeration fix `list_repos` got; both must agree (test: same repo set on a follower)
- [x] 4.2 `get_cfg`: build the CFG from stored source on a diskless daemon, or return `follow_no_disk` — never "no indexed repo could anchor it" for a resolvable node id
- [x] 4.3 `sibling_diff_context`: route through the same `follow_no_disk` guard as `diff_context`
- [x] 4.4 `audit_agent_config`: `follow_no_disk` (or store-backed scan) instead of `files_scanned: 0` clean message on a diskless follower
- [x] 4.5 `find_co_changing_symbols`: truthful, consistent unavailability message on daemons that never mine co-change (no "retry shortly")
- [x] 4.6 `generate_wiki`: attempts a disk write on the follower and dies on the filesystem ("create wiki root: mkdir wiki: permission denied") — the write seal must cover it (`follow_no_disk` or a seal error), the OS permission error must never be the backstop
- [x] 4.7 Unbound project enumeration parity: `query_project` reports `available_projects: []` and `get_active_project` returns empty while `list_repos` serves 20 repos — same enumeration gap as `workspace_info` (4.1); all unbound-mode surfaces must agree
- [x] 4.8 `find_import_path`: proximity-ranked resolution for ambiguous names (same package > service > repo) and correct `already_imported` detection (test: GortexManager from knowledge/core/kernel.py resolves to knowledge/core, already_imported true)
