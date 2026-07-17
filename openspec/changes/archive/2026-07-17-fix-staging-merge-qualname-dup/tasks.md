# Tasks: fix-staging-merge-qualname-dup

## 1. Investigate

- [x] 1.1 Reproduce the SQLSTATE 23505 on `idx_nodes_qual_name` with `power-sync-template` (fails independently per branch copy); capture the offending duplicate qualified-name rows from staging
- [x] 1.2 Determine what `idx_nodes_qual_name` uniqueness protects — which lookups/joins depend on it — to decide whether relaxing it is safe

## 2. Decide the fix direction

- [x] 2.1 Choose: dedup-on-merge (dedupe staging rows by the constraint key before merge) vs. constraint scope (add a discriminator to the unique key). Record the decision + rationale in design.md

## 3. Implement + verify

- [x] 3.1 Apply the chosen fix in `internal/graph/store_pg/` (bulk-load merge and/or `idx_nodes_qual_name` DDL + a schema migration if the index changes)
- [x] 3.2 Test: a repo with internally-duplicated qualified names merges cleanly (no 23505) and no unintended node loss/merge; existing conformance stays green
