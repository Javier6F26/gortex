# Design: fix-contracts-hydration-residuals

## Context

The v0.68.0 store-backed registry fallback resolved the "no contract registry available" error but hydrates zero entries from 125 persisted contract nodes (live evidence: `contracts {all_repos:true}` → `by_repo: {}, total: 0`; `graph_stats.by_kind.contract` → 125; `contracts_orphans` reads all 125 from the same store and — with an empty registry to match against — flags every one "provider with no counterpart"). Separately, the inspections runner drops the whole `contracts_orphans` block from `results` when `max_per_inspection=200` while `summary` still says 125.

## Goals / Non-Goals

**Goals:** registry/node parity on the follower; internally consistent inspection payloads; trustworthy orphan results.

**Non-Goals:** contract extraction changes on the writer; new contract kinds.

## Decisions

1. **Diagnose hydration with a parity test, not by eye**: a pg-backed test seeds contract nodes exactly as the indexer persists them (same meta shape) and asserts the fallback registry materializes each — the likely culprits are a kind/meta filter mismatch or a repo-prefix scoping applied during hydration that excludes everything in unbound mode.
2. **Reconcile summary from returned blocks**: build `summary.by_inspection` from the blocks actually emitted (plus explicit truncation counts), instead of counting violations before response assembly — makes the cap=200 divergence structurally impossible.
3. **Gate orphans on registry health**: if hydration yields zero entries while contract nodes exist, `contracts_orphans` reports skipped/unavailable (the requirement added in the previous change) rather than 100%-orphan output.

## Risks / Trade-offs

- [Hydration parity may expose meta gaps in older stores] → the parity test doubles as the detector; if old nodes lack fields, backfill on next writer sync and document the pre-backfill behavior.
- [Summary-from-blocks changes summary semantics under caps] → keep reporting the full per-inspection totals alongside `returned` and `truncated` so callers see both.

## Open Questions

- Why did cap=50 emit the block but cap=200 dropped it? (Reproduce under a response-size budget — suspected byte-budget elision that spares the summary.)
