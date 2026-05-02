# `internal/contracts`

API-contract extraction and matching — HTTP routes, gRPC services,
GraphQL operations, message topics, WebSocket endpoints, env-var
references, OpenAPI specs, and dependency-injection bindings.

## Workspace boundary

Every `Contract` carries `WorkspaceID` and `ProjectID` slugs along
with `RepoPrefix`. The matcher (`Match`) buckets contracts by the
tuple `(EffectiveWorkspace, EffectiveProject, ID, Role)` before
pairing — providers and consumers in different workspaces *or*
different projects never pair, no matter how identical their IDs
look.

`EffectiveWorkspace`/`EffectiveProject` fall back to `RepoPrefix` when
the explicit slug is empty. The "missing → repo-name" default
preserves backwards compatibility for callers that haven't started
populating slugs yet — single-repo single-project setups still pair
correctly.

The boundary is what makes `gortex contracts check` for `tuck` not
spuriously pair with `personal` even when both define
`POST /api/auth/login`.

### Explicit shared workspace (cross-repo pairing)

To pair providers and consumers across repositories that are part of
one logical service, declare the same `workspace` slug in each repo's
`.gortex.yaml`:

```yaml
# tuck-api/.gortex.yaml
workspace: tuck
project: tuck

# tuck-app/.gortex.yaml
workspace: tuck
project: tuck
```

With matching `workspace` + `project`, contracts in `tuck-api` and
`tuck-app` pair as a `CrossRepo: true` link. Different `project`
slugs (e.g. `services/api` vs `services/worker` in a monorepo) make
the match drop to orphans by design.

## Wrapper inlining

`InlineWrappers` walks consumer contracts whose path is a single
parameter placeholder (the signature of a `request(path, ...)`
helper) and re-extracts each caller as its own consumer contract
with the literal path. Inlined contracts inherit the caller's
`WorkspaceID`/`ProjectID` — without that, the inlined consumer
would default to its repo's bucket and miss its provider when the
two repos share an explicit workspace.
