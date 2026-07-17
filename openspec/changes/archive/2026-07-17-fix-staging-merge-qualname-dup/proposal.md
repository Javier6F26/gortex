# Proposal: fix-staging-merge-qualname-dup

## Why

During the 2026-07-17 albatros-intelligence cold index incident (the same run that surfaced the embedding-dimension bug, now fixed in `adaptive-embedding-dimensions`), the PostgreSQL bulk loader's staging→live merge failed on some repos with:

```
store_pg: merge nodes from staging: ... duplicate key value violates unique constraint "idx_nodes_qual_name" (SQLSTATE 23505)
```

The failure is reproducible with `power-sync-template` and triggers **independently per branch copy** — repos that carry the same qualified name more than once internally (e.g. multiple worktree/branch copies of the same tree, or generated code that legitimately repeats a qualified name) collide on `idx_nodes_qual_name` when the staging rows are merged into the live `nodes` table. A single offending repo aborts its whole merge, so that repo indexes with missing nodes while the rest of the workspace proceeds.

This is unrelated to embedding dimensions; it was tracked alongside the embedding change for incident visibility and is extracted here so it stays actionable rather than buried in the archived change.

## What Changes

The fix approach is **not yet decided** — this change captures the bug and the two candidate directions to evaluate:

- **Dedup-on-merge**: deduplicate staging rows by the constraint's key columns before the merge (last-write-wins, or a deterministic tie-break), so an internally-duplicated qualified name cannot violate the unique index.
- **Constraint scope**: reconsider whether `idx_nodes_qual_name` should be `UNIQUE` at its current scope, or whether the qualified-name key needs an additional discriminator (repo prefix, branch/worktree, or node id) so legitimately-repeated qualified names coexist.

A decision requires understanding what `idx_nodes_qual_name` protects (which lookups rely on its uniqueness) before relaxing it — dedup-on-merge is likely the lower-risk path if the index is a genuine invariant.

## Impact

- **Code**: `internal/graph/store_pg/` bulk-load staging→live merge path (`bulk_load.go` / the node merge SQL) and/or the `idx_nodes_qual_name` DDL in `schema.go`.
- **Operational**: affected repos currently index with an aborted node merge (silent partial data for that repo). The fix restores complete indexing for repos with internally-duplicated qualified names.
- **Reproduction**: `power-sync-template` (fails independently per branch copy).
