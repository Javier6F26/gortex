# Design: fix-follower-contract-registry

## Context

Live v0.67.0 evidence (postgres follower behind kmcp, store with `contract: 125` in `graph_stats`): `contracts` → "no contract registry available — index a repository first"; `contracts_orphans` inspection → 0 violations (silent false-clean). `effectiveContractRegistry()` (internal/mcp/server.go) prefers `multiIndexer.MergedContractRegistry()` / `indexer.ContractRegistry()`, both index-time products; the follower never indexes, and the persisted contract nodes are never consulted. This is the second instance of the "index-time in-memory state absent on follower" class (first: BM25 readiness gate, fixed in v0.67.0 via `search.StoreRouted`).

## Goals / Non-Goals

**Goals:**
- Contract-backed tools work on any daemon serving a store that contains contracts.
- No registry-dependent surface can return a silent clean when the registry is absent.

**Non-Goals:**
- Changing how the writer builds/persists contracts during indexing.
- Contract extraction improvements.

## Decisions

1. **Store-backed registry fallback in `effectiveContractRegistry()`**: when indexer-held registries are nil/empty, build a registry from the store's `kind=contract` nodes (they carry the contract metadata the registry needs) and cache it; invalidate on graph-change notification (same signal the follower already consumes for staleness). Alternative — hydrating the indexer's registry at boot — couples follower boot to an indexer that otherwise does nothing.
2. **Audit the class, not just this instance**: sweep other `effective*`/indexer-held state consulted by MCP handlers (guards, scopes, notebook registries, agent registries...) for the same follower gap; fix or explicitly document each. A one-line audit table in the change is the deliverable.
3. **Inspections carry availability**: the inspections runner marks a registry-dependent inspection `skipped` with a reason when its data source is unavailable, so callers (kmcp punch lists) can tell clean from blind.

## Risks / Trade-offs

- [Registry built from nodes may miss fields the index-time registry had] → compare registries writer-vs-follower on the same store in a test; persist any missing field on the contract node first.
- [Cache staleness on long-lived followers] → reuse the existing graph-invalidation subscription; worst case serve one stale read.

## Audit (Decision 2 / task 3.1): index-time & in-memory state on the follower

Sweep of `Server` state that is either populated by an indexing run or held
in-process, and how each behaves on a diskless read-only follower. Status
is one of **works-from-store** (recomputed/served from the shared graph),
**fixed** (this change), or **N/A** (not index-derived, or correctly
sealed).

| State / handler | How it's populated | Follower behaviour | Status |
|---|---|---|---|
| `contractRegistry` / `contracts`, `api_impact`, `change_contract`, `generate_wiki` contract block | indexing (`MergedContractRegistry` / `Indexer.ContractRegistry`) | store-backed fallback rehydrates from persisted `kind=contract` nodes (`effectiveContractRegistry` → `storeContractRegistry`) | **fixed** |
| `contracts_orphans` inspection | reads the contract registry | routed through `effectiveContractRegistry`; reported **skipped** (not silent `total:0`) when the registry is absent | **fixed** |
| `get_cfg` | disk source read | reads through `sourceLinesForNode` (overlay → disk → store blob); `follow_no_disk` when no blob | **fixed** |
| `workspace_info`, `get_active_project`, `query_project` | config / session workspace | unbound branch enumerates `graphRepoEntries()` — the same source `list_repos` uses; all four agree | **fixed** |
| `sibling_diff_context`, `audit_agent_config`, `generate_wiki` | git working tree / disk | `follow_no_disk` guard (matches `diff_context`); `generate_wiki` sealed before any mkdir | **fixed** |
| `find_co_changing_symbols` | git-log mine (`mineCoChange`) | never mines on a follower (no tree; prewarm inert); serves only writer-persisted `EdgeCoChange` edges, with a stable persisted-edges-only note — no false "retry shortly" | **fixed** |
| `find_import_path` | graph symbols | proximity-ranked (package > service > repo > cross); same-package `already_imported` | **fixed** |
| communities / clusters (`incrementalCommunities`, `getCommunitiesWithStats`) | `runAnalysis`, but lazily recomputed on token mismatch | recomputed on demand from the store graph — the `(NodeCount, EdgeCount, EdgeIdentityRevisions)` token drives a lazy rebuild | **works-from-store** |
| `guardRules` / `check_guards` | `.gortex.yaml` config (NOT indexing) | config-driven, evaluated against the graph node set the store serves | **works-from-store** |
| `agentReg` / `agent_registry` | in-process coordination (presence, cursors, advisory locks) | in-memory session/coordination state, not index-derived | **N/A** |
| notebook tools | session / overlay buffers | `notebook_save` is in `followDenyTools` (write sealed); reads are session state | **N/A (sealed)** |
| saved `scopes` (`scopeStore`) | JSON-file-backed user convenience | user/session config, not index-derived; a diskless follower is not a scope-authoring surface | **N/A** |
| pre-warmed analysis fields consulted directly (`s.processes`, `s.pageRank`, `s.hits`, `s.hotspots`, `s.autoConcepts`) | `runAnalysis` only | empty on a follower that never runs `runAnalysis`; the live v0.67.0 sweep did not report the analyze-family tools failing, so a lazy-recompute seam for these (mirroring `incrementalCommunities`) is recorded as a **follow-up**, not fixed here (out of this change's proposal scope) | **follow-up** |

## Open Questions

- Which other tools share this gap? Answered by the audit table above.
- The pre-warmed analysis fields (`s.processes` et al.) are the one remaining
  member of this class without a store-recompute seam. Tracked as a follow-up
  because the exhaustive live sweep did not surface an analyze-family failure;
  if one appears, the fix mirrors `incrementalCommunities`' lazy path.
