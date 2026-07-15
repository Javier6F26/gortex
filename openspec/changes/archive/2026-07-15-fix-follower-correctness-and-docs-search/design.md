# Design: fix-follower-correctness-and-docs-search

## Context

All findings come from live testing against a v0.65.1 follow-mode daemon (PostgreSQL backend, multi-repo store with ~102k nodes) proxied by the Albatros kmcp server. Reproductions:

- **Docs recall**: `search_symbols(corpus=docs, repo=master-ssot-vault, query="constitución técnica")` → hit (heading match). Same call with `query="branch_track repository_url"` (verbatim in `constitution.md` body) → zero, via both `corpus=docs` and `kind=doc`. Conclusion: doc-section nodes are searchable by heading/slug tokens only; bodies are not in the matched channel. The pipeline plumbing (`mergeDocChannel` in `internal/mcp/search_corpus.go`, corpus post-filter in `tools_core.go`) is correct — the gap is in what text the FTS/BM25 channel indexes for `KindDoc` nodes.
- **detect_changes**: on the diskless follower returns `{"risk":"NONE","summary":"no indexed symbols affected"}` while `review`/`review_pack` correctly error `follow_no_disk` (guard in `internal/mcp/tools_follow_source.go`).
- **list_repos**: returns `{"mode":"unbound","repos":[]}` through the proxy.
- **dead_code**: flags `__call__`, `__aenter__`, `__aexit__`, `__post_init__` (inspector in `internal/analysis/connectivity.go`).
- **provenance**: no per-repo indexed-SHA surface anywhere.
- **ranking**: concept queries return `param`/`generic_param`/lockfile `module` nodes at the head.

## Goals / Non-Goals

**Goals:**
- Docs queries match section bodies; descriptions tell the truth.
- No tool on a diskless follower fabricates an empty-but-plausible answer.
- Clients can compare their local HEAD against the indexed SHA per repo.
- Fewer false positives in `dead_code`; less noise in concept-query heads.

**Non-Goals:**
- Working-tree review capabilities on the follower (kmcp hides those tools by design; the pasted-diff path of `review`/`review_pack` stays out of scope).
- Vector/semantic search for docs — this is about the lexical channel's coverage.
- Any writer-path or indexing-pipeline redesign beyond adding body text and provenance metadata.

## Decisions

1. **Index doc-section bodies into the existing symbol FTS channel** (preferred) rather than standing up a second docs-only index. The content index (`content_fts`, `data_class=content`) already demonstrates the body-indexed pattern; extending the indexer to write section body text into the searchable text of `KindDoc` nodes reuses `mergeDocChannel` unchanged. Alternative considered: route `corpus=docs` through the ContentSearcher — rejected because Markdown prose sections are deliberately a distinct channel from extracted content chunks.
   - Requires a reindex/migration of existing stores to backfill bodies; must degrade gracefully (heading-only matching) for stores indexed by older writers.
2. **Reuse the `follow_no_disk` guard for `detect_changes`** — one-line composition with the existing `followerGitGuard` (`tools_follow_source.go`), matching `review`/`review_pack` semantics. No new error vocabulary.
3. **`list_repos` unbound mode falls back to graph enumeration**: when no workspace is bound, enumerate distinct repo prefixes from the store (the graph already answers this for scope resolution). Keep `mode:"unbound"` in the envelope for observability; populate `repos` with `{name, branch?, last_synced_sha?, last_synced_at?}`.
4. **Provenance is writer-recorded metadata, follower-served**: the writer stamps `{sha, timestamp}` per repo at sync completion into store metadata; `daemon status` and `list_repos` read it. Followers never compute it (they have no git); they serve what the writer wrote.
5. **Dunder exclusion lives in the inspector, not the graph**: `dead_code` skips methods matching `__*__`. Alternative — synthesizing implicit-call edges at index time — is more faithful but far larger; not worth it for this fix.
6. **Concept-query noise handled in the rerank, not hard filters**: for `query_class=concept`, apply a strong negative prior to `param`/`generic_param` kinds and to `module` nodes whose file is a known lockfile (`package-lock.json`, `yarn.lock`, `go.sum`, ...). Explicit `kind:` requests bypass the prior. Hard exclusion rejected: an explicit search for a parameter name must stay possible.

## Risks / Trade-offs

- [Body indexing grows the FTS index] → Section bodies are bounded (sections, not whole files); measure index size on the Albatros store before/after; cap indexed body length per section if needed.
- [Backfill requires reindexing existing stores] → Ship as a lazy migration: new syncs write bodies; `daemon status` exposes a `docs_bodies_indexed` capability flag so clients (kmcp) can detect coverage.
- [list_repos graph enumeration could be slow on huge stores] → Repo prefixes are already materialized for scope resolution; serve from that cache.
- [Rerank priors can over-suppress] → Gate on `query_class=concept` only; keep nodes retrievable via explicit `kind:`.

## Migration Plan

1. Land guard + inspector + rerank changes (no store format impact) in a patch release.
2. Land provenance stamping + body indexing in the writer; bump store schema version tolerantly (old followers ignore new metadata).
3. Reindex the shared Albatros store (writer resync); followers pick the new data up without redeploy.
4. Rollback: all changes are additive or guard-tightening; reverting the binary restores prior behavior without store repair.

## Open Questions

- Should heading matches keep a rank boost over body matches, and how strong? (Spec requires only "at least as high".)
- Exact lockfile list for the module-noise prior — derive from existing lockfile detection in the indexer?
- Does kmcp's `vault_search` switch back from `kind=doc` to `corpus=docs` once bodies are indexed? (Both will work; `corpus=docs` is the intended API.)
