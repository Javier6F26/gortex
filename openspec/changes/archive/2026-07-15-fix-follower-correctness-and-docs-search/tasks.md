# Tasks: fix-follower-correctness-and-docs-search

## 1. Follower tool honesty (no store impact — ship first)

- [x] 1.1 Apply the `follow_no_disk` guard (`internal/mcp/tools_follow_source.go`) to `detect_changes` in `internal/mcp/tools_analysis.go`; add a follower-mode test asserting the error (extend `follow_integration_test.go`)
- [x] 1.2 Make `list_repos` enumerate repo prefixes from the store when no workspace is bound; keep `mode:"unbound"`; add a multi-repo unbound test
- [x] 1.3 Audit remaining git-dependent tools for the same silent-empty pattern (`get_recent_changes`, `get_churn_rate`, `compare_branches`, ...) and guard or document each

## 2. Inspection accuracy

- [x] 2.1 Skip `__*__` methods in the `dead_code` inspection (`internal/analysis/connectivity.go`); unit tests: dunders with zero refs not flagged, plain methods still flagged (`deadcode_test.go`)

## 3. Concept-query ranking

- [x] 3.1 Add a negative rerank prior for `param`/`generic_param` kinds on `query_class=concept` queries; explicit `kind:` bypasses it
- [x] 3.2 Add lockfile detection for `module` nodes (`package-lock.json`, `yarn.lock`, `go.sum`, `Cargo.lock`, ...) and apply the same prior
- [x] 3.3 Regression test: a concept query matching both methods and params returns methods in the head

## 4. Docs body search

- [x] 4.1 Confirm root cause in the indexer: verify what text is written to the FTS channel for `KindDoc` prose-section nodes (heading/slug vs body)
- [x] 4.2 Index section body text into the searchable text of `KindDoc` nodes (bounded per-section length; measure FTS size delta on a large store)
- [x] 4.3 Regression tests: body-only phrase found via `corpus=docs` and via `kind=doc`; heading matches ranked at least as high
- [x] 4.4 Expose a `docs_bodies_indexed` capability flag in `daemon status` so clients can detect pre-backfill stores
- [x] 4.5 Update the `search_symbols` tool description so the docs-corpus claim matches actual behavior (in the same release as 4.2, or corrected immediately if 4.2 slips)
- [x] 4.6 Compute `scope_note` candidate counts after all non-scope filters (corpus/kind/flavor) in `tools_core.go`; test: docs query + repo scope with zero survivable candidates outside produces no widen hint

## 5. Index provenance

- [x] 5.1 Writer: stamp `{sha, timestamp}` per repo/branch into store metadata at sync completion
- [x] 5.2 Serve provenance in `daemon status` and in each `list_repos` entry (follower reads writer-stamped metadata; never computes it)
- [x] 5.3 Follow-mode integration test: provenance written by the writer is visible through a follower

## 6. Verification against the live Albatros store

- [ ] 6.1 Resync the shared store with the new writer; re-run the failing live queries (`branch_track repository_url` corpus=docs; kmcp `vault_search` "convenciones commits ramas pull request") and the `detect_changes`/`list_repos` follower calls
- [ ] 6.2 Notify kmcp: decide whether `vault_search` returns to `corpus=docs` (both paths must pass) and whether the kmcp denylist keeps `detect_changes`
