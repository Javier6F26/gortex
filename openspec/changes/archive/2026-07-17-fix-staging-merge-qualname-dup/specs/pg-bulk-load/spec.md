## ADDED Requirements

### Requirement: Duplicate qualified names must not abort the node merge
The PostgreSQL node store SHALL treat `qual_name` as a non-unique lookup key. Distinct nodes (different `id`) MAY share a `qual_name` — branch/worktree copies of the same tree and generated or repeated code legitimately produce this — and neither bulk-load merge path SHALL fail on such duplicates. The `qual_name` index SHALL NOT be unique, in the live schema, in the bulk staging table, and after any destructive cold-swap.

#### Scenario: Cold-swap load with internally-duplicated qualified names

- **WHEN** a first (empty → full) bulk load stages multiple nodes with distinct ids and the same non-empty `qual_name`
- **THEN** the destructive swap SHALL complete without SQLSTATE 23505
- **THEN** every such node SHALL be present in the live table (no silent dedup)

#### Scenario: Incremental merge with duplicate qualified names

- **WHEN** a subsequent (non-destructive) bulk load merges staging nodes whose `qual_name` already exists on a different id in the live table
- **THEN** the `INSERT … SELECT … ON CONFLICT (id)` merge SHALL complete without SQLSTATE 23505
- **THEN** the pre-existing and newly merged nodes SHALL both persist

#### Scenario: Existing deployment with a unique qual_name index migrates

- **WHEN** a store upgraded from a version whose `qual_name` index is unique (under any index name, including an auto-generated one left by a prior cold-swap) applies the schema migration
- **THEN** the migration SHALL drop the unique index by definition and recreate a non-unique index
- **THEN** subsequent merges with duplicate qualified names SHALL succeed
