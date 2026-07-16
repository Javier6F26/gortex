# Proposal: fix-follower-doc-body-search

## Why

v0.66.0 implemented doc-body search at the store level (`store_pg.searchDocBodies`, merged into `SearchSymbols`/`SearchSymbolBundles`, with passing pg tests) — but live verification against the deployed follower shows body queries still return zero through MCP. The `docs-corpus-search` requirement "Docs-corpus queries match section body text" is NOT met end-to-end; the residual gap is in the follower's search wiring and in what the indexer stores.

Live evidence (v0.66.0+0ccff275, follower + writer, vault resynced):
- Direct SQL against the shared store finds the sections: `to_tsvector('english', meta->>'section_text') @@ plainto_tsquery('Síntesis vinculante convenciones ecosistema')` → 4 hits (one per vault copy). All 1056 `master-ssot-vault` doc nodes carry `section_text`.
- The same phrase through `search_symbols` with `corpus=docs` → 0 hits. Without the corpus filter → 15 hits, all heading/name nodes, no `KindDoc` body hit — so the engine's backend never surfaces body matches; the corpus post-filter then drops everything.
- Identifier phrases can never match: `section_text` is stored with underscores stripped (`constitution_read()` → `constitutionread()`, `branch_track` → `branchtrack`), so "branch_track repository_url" fails at the data layer even via SQL.

## What Changes

- Route the follower daemon's live search path through the store's body-aware channel: the query engine's backend on a postgres follower must include `searchDocBodies` results (via `graph.SymbolSearcher` routing or an explicit doc-body merge in `mergeDocChannel`), not just the name-token index.
- Preserve identifier tokens in `section_text`: stop stripping underscores (and any other identifier-significant punctuation) when extracting prose-section bodies in the indexer, then backfill via resync.
- Add a follow-mode end-to-end regression test: body-only phrase (with underscores) → `search_symbols corpus=docs` hit through the MCP surface, not only a store unit test.

## Capabilities

### New Capabilities

(none)

### Modified Capabilities

- `docs-corpus-search`: the body-matching requirement gains explicit end-to-end scope (through the daemon's MCP search surface in follow mode) and an identifier-token preservation requirement for stored section text.

## Impact

- `internal/query/engine.go` / `internal/search` backend wiring for the postgres follower (gatherBackendCandidates / bundle path).
- `internal/mcp/search_corpus.go` `mergeDocChannel` (or equivalent) so the doc channel pulls from the body-aware store search.
- Indexer prose-section extraction (`section_text` normalization) + a resync/backfill of existing stores.
- Verification: reproducible probe battery exists (kmcp-side, `gortex066_probe.py` pattern) — probe 1a is the acceptance check.
