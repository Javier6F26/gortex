# `internal/resolver`

Resolves unresolved edges in the graph — symbol references and
imports recorded as `unresolved::...` placeholders by the per-language
extractors get rewritten to point at real graph nodes once those
nodes exist.

## Cross-repo + cross-workspace boundary

`CrossRepoResolver` searches across repository boundaries when no
same-repo candidate exists. With a `CrossWorkspaceDepLookup` wired
via `SetCrossWorkspaceDepLookup`, candidates from a *different*
workspace are accepted only when:

1. The source workspace declares the target workspace in its
   `cross_workspace_deps`, AND
2. For import edges, the import path matches a declared module
   prefix (longest match wins).

For function/method-call edges (no import path available) the
workspace-pair declaration alone is sufficient. The whole point of
this layer is that an unresolved call into another workspace
silently fails to resolve unless the user opted in.

When the lookup is unset (legacy callers), the resolver falls back
to permissive cross-repo lookup: any cross-repo candidate is fair
game. This keeps existing tests and single-workspace setups working
without code changes.

## Wiring

The `internal/indexer.MultiIndexer` builds the lookup from each
tracked repo's `.gortex.yaml::cross_workspace_deps` and sets it on
the resolver in two places:

- `MultiWatcher.NewMultiWatcher` for live-edit re-resolution.
- `MultiIndexer.IndexAll` after the per-repo indexing loop completes.
  Also re-run by `MultiIndexer.RunGlobalResolve` after warmup.

If neither path runs (e.g. a single-repo `Indexer.ResolveAll`), the
boundary is permissive.
