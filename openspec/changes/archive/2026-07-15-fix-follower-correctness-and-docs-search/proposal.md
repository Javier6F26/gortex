# Proposal: fix-follower-correctness-and-docs-search

## Why

Live testing of a follow-mode daemon (v0.65.1, PostgreSQL backend) behind the Albatros kmcp proxy surfaced six defects that make the follower lie or go blind: docs-corpus searches only match section headings — a query on body-only text (e.g. `branch_track repository_url`, verbatim in `constitution.md`) returns zero results even though the tool description promises "a prose query matches by body text", breaking every docs/vault search built on it; `detect_changes` silently reports "no changes, risk NONE" on a diskless follower instead of erroring; and `list_repos` returns an empty unbound envelope through the proxy. Clients also have no way to know which commit the index reflects, the dead-code inspector flags dunder methods, and concept queries rank `param` noise above real symbols.

## What Changes

- Make docs-corpus retrieval match prose-section BODY text, not just heading/slug tokens. Verified live: heading queries ("ramas agrupadoras", "constitución técnica") hit; body-only queries ("branch_track repository_url") return zero via both `corpus=docs` and `kind=doc`. Either index section bodies into the searchable channel or route docs queries through a body-backed index — and align the tool description with actual behavior.
- Compute `scope_note` candidate counts AFTER all filters (corpus included), so the note never claims "N candidates outside scope" that the remaining filters would drop anyway.
- Make `detect_changes` fail with the `follow_no_disk:` error on a follower without a git working tree — same guard as `review`/`review_pack` — instead of returning an empty "risk NONE" result.
- Make `list_repos` list the repositories present in the graph when the daemon serves a multi-repo store without a bound workspace (follower/unbound mode), instead of `{"mode":"unbound","repos":[]}`.
- Expose index provenance: last-synced commit SHA and sync timestamp per repo/branch in `daemon status` and `list_repos`, so a client can compare against its local HEAD.
- Exclude dunder methods (`__call__`, `__aenter__`, `__aexit__`, `__post_init__`, and `__*__` generally) from the `dead_code` inspection — they are invoked implicitly.
- Demote or exclude `param`/`generic_param` nodes and package-lock `module` nodes from concept-class query results in the rerank pipeline.

No breaking changes: all fixes tighten existing contracts or add fields.

## Capabilities

### New Capabilities
- `docs-corpus-search`: `corpus=docs` returns Markdown prose-section nodes; scope notes are computed post-filter.
- `follower-tool-honesty`: git-dependent tools on a diskless follower error with `follow_no_disk` rather than fabricating empty results; `list_repos` reports graph repos in unbound mode.
- `index-provenance`: per-repo/branch last-synced SHA + timestamp surfaced in `daemon status` and `list_repos`.
- `inspection-accuracy`: dead-code inspection ignores implicitly-invoked dunder methods.
- `concept-query-ranking`: concept-class searches suppress `param`/`generic_param`/lockfile-module noise.

### Modified Capabilities

(none — `openspec/specs/` is empty; these are the first specs for the affected surfaces)

## Impact

- `internal/mcp/search_corpus.go`, doc-channel search path and scope-note assembly (`tools_search_*`).
- `internal/mcp/tools_follow_source.go` guard applied to `detect_changes` (`tools_analysis.go`).
- `internal/mcp/server.go` / workspace listing path for `list_repos` unbound mode.
- `internal/analysis/connectivity.go` (dead_code inspector).
- Rerank pipeline weights/filters for concept queries (`internal/search/`).
- Index metadata: persistence of per-repo sync SHA/timestamp; `daemon status` output.
- Consumers: kmcp vault_search currently works around (1) via `kind:doc` and hides (2) behind a denylist — both workarounds can stay; this change fixes the daemon for every other client.
