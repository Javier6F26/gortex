## Context

The PostgreSQL graph store backend (`internal/graph/store_pg/`) mirrors the SQLite backend (`internal/graph/store_sqlite/`) but contains five defects discovered during a systematic comparison. Three are critical (silent correctness failures and data races), two are moderate (runtime crashes and data errors under specific conditions).

The bundle cache was ported from SQLite but never wired up — `SetBundleFingerprints` is a no-op and `SearchSymbolBundles` bypasses the cache entirely. The bulk load state uses a package-level `var` instead of a `Store` field, making it unsafe for concurrent store instances. The `AppendContent` method has two bugs: placeholder numbering breaks when NodeID-empty items are skipped, and raw content bytes containing non-UTF-8 sequences crash PostgreSQL's generated-tsvector path.

## Goals / Non-Goals

**Goals:**
- Wire `SetBundleFingerprints` and `SearchSymbolBundles` so the bundle cache is functional OR remove it cleanly
- Move `var bulk *bulkState` into the `Store` struct
- Fix `AppendContent` placeholder numbering
- Fix `AppendContent` UTF-8 crash to make content indexing resilient to binary file contents
- All fixes must preserve SQLite parity (no behavioural drift)

**Non-Goals:**
- Adding new capabilities to either backend
- Refactoring the SQLite backend
- Performance tuning beyond what the cache fix restores

## Decisions

### Decision 1: Keep the bundle cache and wire it correctly (vs. removing it)

**Chosen:** Wire the cache.

The comment in `store_fts.go` says PG is "already fast" without the cache, but for large query volumes (rerank pipeline, `distill_session`, etc.) the cache saves one batched edge fetch per package. The fingerprint infrastructure (daemon analysis pass → `SetBundleFingerprints`) already exists and is cheap to honour. Removing the cache now would risk re-adding it later — wiring it is simpler.

The SQLite design keys the cache by `node.ID` and stores one `SymbolBundle` per entry, validated against the node's package fingerprint. The PG design keys by `pkgKey` and stores batches per package. These are different granularities but both are correct — the PG approach is actually more efficient for batched access.

**Changes:**
1. Rename `bundleCache.lookup(pkgKey string)` search to match how the SQLite version is called: change `SearchSymbolBundles` to consult the cache via `s.bundles.lookup(pkgKey)` style, where pkgKey is derived from each hit's file path
2. Replace the no-op `SetBundleFingerprints` with a call to `s.bundles.refresh(fps)` (matching SQLite's approach)
3. Remove the private `setBundleFingerprints` dead method in `bundle_cache.go`

Actually, simpler approach: follow SQLite exactly. Change `SearchSymbolBundles` to:
```
for each hit:
  pkgKey = bundlePackageKey(hit.FilePath)
  if cached = s.bundles.lookup(pkgKey); cached hit → use it
  else: query normally, then s.bundles.store(...)
```

Wait — the PG cache is batch-keyed per `pkgKey`, not per-node. So lookup by `pkgKey` returns all bundles for a package at once. The simplest correct approach:

1. In `SearchSymbolBundles`, group hits by their `bundlePackageKey(filePath)`. For each group that has a cache hit, serve cached bundles (skipping DB). For cache misses, run the existing query path.
2. After the miss path, store the result in the cache under its `pkgKey`.

This matches the PG cache's batch design without reworking the cache to per-node granularity.

### Decision 2: UTF-8 sanitization location

**Chosen:** Sanitize in `store_pg`'s `AppendContent`, with an optional guard in `content_split.go`.

- **PG layer** (`AppendContent`): `strings.ToValidUTF8(item.Body, "�")` right before binding the parameter. This is the minimum fix — it catches the problem at the PostgreSQL interface boundary.
- **Content layer** (`content_split.go` or `collectContentItems`): Adding `strings.ToValidUTF8` here would protect both backends, but SQLite doesn't need it and it would mask bugs in content extractors. We'll add a `strings.Valid(body)` check with a warning log in `collectContentItems` for observability, but only sanitize in the PG backend.

### Decision 3: Placeholder offset fix

**Chosen:** Replace `offset := j * 5` with `offset := len(valueStrings) * 5`.

The SQLite version uses `?` placeholders so it doesn't have this problem. PG uses numbered `$N` placeholders and must compute correct offsets when items are skipped. The fix is one line.

## Risks / Trade-offs

| Risk | Mitigation |
|------|-----------|
| [Bundle cache] Wire incorrectly → stale search results served | The fingerprint discipline already ensures correctness: a stale bundle is detected by fingerprint mismatch on every lookup. No risk of serving stale data. |
| [Bundle cache] Cache adds latency on miss | Cache miss falls through to the existing query path — no regression. The lookup/miss overhead is a map read + one batched query. |
| [UTF-8] `strings.ToValidUTF8` with replacement character loses information | Better than crashing the entire index. The replacement character `�` (U+FFFD) is the standard Unicode practice. |
| [Bulk singleton] Miss any reference to `var bulk` | Grep for `bulk` in `store_pg/` — it appears in `bulk_load.go`. Replacing `var bulk` with `s.bulk` requires changing the field type, `BeginBulkLoad`/`FlushBulk`, and `AddBatchBulk`. |
| [EdgesWithUnresolvedTarget] Different query shape could miss edges | The PG version uses `LIKE 'unresolved::%' OR LIKE '%::unresolved::%'` — this is functionally a superset of SQLite's range-scan + LIKE. No edges are missed. |
