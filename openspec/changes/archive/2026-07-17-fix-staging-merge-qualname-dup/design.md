# Design: fix-staging-merge-qualname-dup

## Context

`idx_nodes_qual_name` is a **global** partial unique index:

```sql
CREATE UNIQUE INDEX idx_nodes_qual_name ON nodes(qual_name) WHERE qual_name <> '';
```

The bulk loader has two merge paths, both of which enforce this uniqueness and
both of which fail on internally-duplicated qualified names:

- **Destructive/cold path** (`bulk_load.go:278`): builds
  `CREATE UNIQUE INDEX ON <staging>(qual_name) WHERE qual_name <> ''` on the
  staging table before the atomic swap. If staging carries the same `qual_name`
  on two different `id`s, the index build fails 23505.
- **Safe/incremental path** (`bulk_load.go:375`): `INSERT INTO nodes … SELECT …
  ON CONFLICT (id) DO UPDATE`. The `ON CONFLICT (id)` handles id collisions, but
  a staging row whose `qual_name` already exists on a **different id** in the
  live table violates the unique index → 23505 ("merge nodes from staging").

Reproduced with `power-sync-template` (fails independently per branch copy).

## Root cause

`qual_name` is **not** globally unique. Distinct nodes legitimately share a
qualified name: multiple worktree/branch copies of the same tree, and generated
or repeated code within a single repo. A globally-unique index on `qual_name`
encodes an invariant the data does not hold in a multi-repo store.

## Decision — drop the uniqueness; keep a plain lookup index

The unique index is demoted to a non-unique index (partial, same predicate):

```sql
CREATE INDEX idx_nodes_qual_name ON nodes(qual_name) WHERE qual_name <> '';
```

**Why this is safe (uniqueness is not a correctness invariant):**

- The only `ON CONFLICT` target in the node write paths is `(id)`
  (`nodeInsertConflict`). Nothing upserts or dedupes on `qual_name`.
- Both qual_name reads already tolerate duplicates: `WHERE qual_name = $1 LIMIT 1`
  (store.go:485) picks one, and `WHERE qual_name = ANY($1)` (store.go:783) is set
  membership. Neither needs the value to be unique — only indexed for fast lookup.

**Alternatives rejected:**

- **Dedup-on-merge** (drop staging rows sharing a `qual_name`): deletes legitimate
  distinct nodes (different `id`, same `qual_name`) → silent data loss. The very
  duplicates that trigger 23505 are real nodes we must keep.
- **Repo-scoped uniqueness** (`UNIQUE(repo_prefix, qual_name)`): still fails on
  branch copies and generated code *within* one repo prefix, so it does not fix
  the reproduced case; it only narrows the window.

Demoting to a non-unique index removes 23505 in **both** merge paths at once,
preserves every node, and leaves lookups on the same index.

## Changes

1. `schema.go` — live DDL: `CREATE UNIQUE INDEX` → `CREATE INDEX` (virgin stores).
2. `bulk_load.go:278` — staging DDL: `CREATE UNIQUE INDEX` → `CREATE INDEX`.
3. Schema migration **V6** — for existing deployments: drop the unique index and
   recreate it non-unique. `DROP INDEX` + `CREATE INDEX` under the existing
   migration advisory lock (idempotent, no table rewrite).

## Risks / Trade-offs

- **[Losing a uniqueness guarantee some future code might assume]** → No current
  code assumes it (verified: no `ON CONFLICT (qual_name)`, lookups tolerate
  duplicates). A future feature needing "the node for a qual_name" must already
  handle multiplicity, since the graph genuinely contains duplicates.
- **[Migration on a store that currently violates uniqueness]** → Impossible by
  construction: the unique index exists only where the data satisfied it so far;
  `DROP INDEX` then `CREATE INDEX` (non-unique) always succeeds and lets the
  previously-failing merges complete on the next index pass.
