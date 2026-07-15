## Why

The PostgreSQL backend (`internal/graph/store_pg/`) was recently implemented to complement the SQLite backend but contains critical defects that cause silent data correctness issues, runtime crashes, and data-race conditions. These bugs must be fixed before the PostgreSQL backend can be used reliably in production — the bundle fingerprint cache is dead code, the bulk-load path has a package-level data race, and content ingestion crashes on non-UTF-8 file contents.

## What Changes

- Fix `SetBundleFingerprints` being a no-op — wire it to actually invalidate the bundle cache so the fingerprint discipline works correctly
- Fix `SearchSymbolBundles` to consult the bundle cache before hitting the database (or remove the dead cache if PG is fast enough without it)
- Fix `var bulk *bulkState` from package-level singleton to per-`Store` field, preventing data races when multiple Store instances coexist
- Fix `AppendContent` placeholder numbering bug — `offset := j * 5` miscomputes parameter indices when items with empty `NodeID` are skipped
- Fix UTF-8 encoding crash in `AppendContent` — sanitize `Body` with `strings.ToValidUTF8` before inserting into PostgreSQL, which enforces valid UTF-8 for `TEXT` columns
- Optionally: review `EdgesWithUnresolvedTarget` query parity with SQLite

## Capabilities

### New Capabilities
- `pg-store-correctness`: Defines the correctness contract for the PostgreSQL graph store backend — all methods must match SQLite semantics, handle edge cases identically, and survive production data loads

### Modified Capabilities
<!-- No existing capability specs are changing — this is a pure implementation fix. -->

## Impact

- `internal/graph/store_pg/store_fts.go` — `SetBundleFingerprints` no-op → proper fingerprint invalidation; `SearchSymbolBundles` cache integration
- `internal/graph/store_pg/bundle_cache.go` — cache invalidation wiring, remove unused code if cache is kept dormant
- `internal/graph/store_pg/bulk_load.go` — `var bulk *bulkState` → `s.bulk` field on `Store` struct
- `internal/graph/store_pg/store_content_fts.go` — placeholder numbering fix + UTF-8 sanitization
- `internal/graph/store_pg/store.go` — add `bulk` field to `Store` struct
- `internal/indexer/content_split.go` — optional: add `strings.ToValidUTF8` guard at source for backend-agnostic protection
- No external API changes, no config changes, no new dependencies
