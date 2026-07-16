# Design: fix-follower-doc-body-search

## Context

The archived `fix-follower-correctness-and-docs-search` change landed doc-body search in `store_pg` (`searchDocBodies`, merged name-first into `SearchSymbols` and `SearchSymbolBundles`; `TestSearchSymbols_DocBodyMatch` passes) and shipped in v0.66.0. Live verification shows the requirement is not met through the daemon:

- SQL `@@ plainto_tsquery` on the live store: body phrases match (4 hits for a constitution body phrase). All doc nodes carry `section_text`.
- MCP `search_symbols` on the same follower: 0 hits with `corpus=docs`; without the filter, only heading/name nodes come back. So `store_pg.SearchSymbols` (body-aware) is not what the follower's query engine consults — the engine's search backend (in-process BM25/Bleve chain, `initialSearchBackend`, whose comment still says "today only store_sqlite" implements `graph.SymbolSearcher` routing) serves name tokens only in follow mode.
- Data-layer defect on top: `section_text` is stored with underscores stripped (`constitution_read()` → `constitutionread()`), so identifier phrases can never match even at SQL level.

## Goals / Non-Goals

**Goals:**
- Body matches reachable through the daemon's MCP search surface in follow mode (and any postgres-backed mode).
- `section_text` preserves identifier tokens; existing stores get backfilled on resync.
- Regression coverage at the MCP layer, not only the store layer.

**Non-Goals:**
- Ranking redesign; heading-vs-body weighting stays "heading at least as high".
- Vector/semantic docs search.
- SQLite parity work beyond keeping its existing behavior green.

## Decisions

1. **Route the follower's backend through `graph.SymbolSearcher` when the store implements it** — `store_pg` now satisfies the interface, so `initialSearchBackend` (and the follow-mode boot path that builds the engine's backend) should take the `SymbolSearcherBackend` branch for postgres too, making `SearchSymbols`/`SearchSymbolBundles` (body-aware) the live search path. Alternative — teaching the in-process BM25 rebuild to index `section_text` on the follower — duplicates the store channel and pays memory for text pg already indexes.
2. **Fix `section_text` extraction, not the query side**: locate the normalization that strips underscores (prose-section extraction / FTS normalization reuse) and preserve identifier characters in the STORED text; FTS tokenizers may still normalize their own index copies. Backfill = writer resync (sections rewrite their nodes); document that pre-backfill stores keep heading-only identifier recall.
3. **Acceptance = MCP-layer test**: extend the follow integration test to assert a body-only, underscored phrase returns its section via `search_symbols corpus=docs` against a postgres store served in follow mode. The kmcp-side probe battery (probe 1a) doubles as the deployed-environment check.

## Risks / Trade-offs

- [Switching the pg backend to store-routed search changes ranking/latency for ALL queries, not just docs] → `SearchSymbolBundles` already exists for the bundle fast path; benchmark before/after on the shared store; keep the in-process path as fallback flag if regression appears.
- [Underscore preservation may alter existing FTS tokenization] → scope the change to the stored `section_text` value; index-side analyzers unchanged.
- [Backfill requires resync of every tracked repo's markdown] → same lazy-migration stance as the archived change; provenance timestamps (`last_synced_at`) tell operators which repos predate the fix.

## Finding (task 1.1 — root cause pinned down)

Reproduced end-to-end against the live deployment (postgres container on host
:5433, a locally-built `gortex daemon --follow` against the shared schema).

- The follow-mode boot path DOES use `initialSearchBackend`: `serverstack.NewSharedServer`
  builds `idx := indexer.New(g, …)` (shared_server.go:294) for both writer and
  follower, and `initialSearchBackend` correctly takes the `graph.SymbolSearcher`
  branch for `store_pg` — the engine's backend is a `SymbolSearcherBackend` wrapping
  the pg store (its comment "today only store_sqlite" is stale). `eng.SetSearchProvider(idx.Search)`
  (shared_server.go:590) wires it in.
- The store layer is correct: a standalone probe (`cmd/gortexprobe`) opening the same
  pg store read-only and calling `store_pg.SearchSymbols` / `SearchSymbolBundles`
  returns the constitution body sections (4 `kind=doc` hits) exactly as the pg unit
  test asserts.
- **The real gap is the engine's readiness gate, not the wiring.** `Engine.SearchSymbolsRanked`
  (engine.go:504) only calls `gatherBackendCandidates` (the store-routed, body-aware
  path) when `e.getSearch().Count() > 0`. `SymbolSearcherBackend.Count()` reports ONLY
  the indexer's `Add`/`Remove` delta-since-construction — and a follower never indexes,
  so its count is 0. The gate therefore fails on the follower and the engine falls back
  to `searchSubstring`, a name-only substring scan that can never surface body text.
  On the writer the indexer's `Add` calls push the count > 0, so the same store path
  works — which is why body search passes on the writer but returns zero on the follower.

**Fix (task 1.2):** mark store-routed backends as always-ready so the `Count() > 0`
gate does not exclude them. A `search.StoreRouted` marker interface implemented by
`SymbolSearcherBackend` (forwarded by `Swappable` / `HybridBackend`) lets the engine
proceed to `gatherBackendCandidates` even at count 0. No separate follower search path
is needed — the existing backend is already correct, it was just being bypassed.

## Open Questions (resolved)

- Follow-mode boot uses `initialSearchBackend` (not a separate path) — resolved above.
- `mergeDocChannel` need not query the store directly: once the readiness gate is fixed
  the store body channel is reachable through the normal engine path.
