## 1. Bundle Cache Wiring

- [x] 1.1 Replace the no-op `SetBundleFingerprints` in `store_fts.go` with a real implementation that delegates to `s.bundles.refresh(fps)` (matching SQLite's pattern)
- [x] 1.2 Remove the unused private `setBundleFingerprints` method from `bundle_cache.go` (the public `SetBundleFingerprints` replaces it)
- [x] 1.3 Add `bundlePackageKey` helper to PG (copied from SQLite) to derive `pkgKey` from a node's `FilePath`
- [x] 1.4 Modify `SearchSymbolBundles` in `store_fts.go` to group hits by package, check the cache, serve cached bundles, and store freshly-computed bundles
- [x] 1.5 Verify that the SQLite and PG `SearchSymbolBundles` return identical results — TestConformance/SymbolBundleSearcher + TestSymbolSearch_BundleSearch ambos PASS contra PG

## 2. Bulk Load State Fix

- [x] 2.1 Add `bulk *bulkState` field to the `Store` struct in `store.go`
- [x] 2.2 Remove `var bulk *bulkState` from `bulk_load.go`
- [x] 2.3 Update `BeginBulkLoad()` to set `s.bulk = &bulkState{}` instead of the package-level var
- [x] 2.4 Replace `bulk.nodes` / `bulk.edges` references in `AddBatchBulk` with `s.bulk.nodes` / `s.bulk.edges`
- [x] 2.5 Update `FlushBulk()` to read from `s.bulk` and set `s.bulk = nil` on completion

## 3. AppendContent Placeholder Fix

- [x] 3.1 In `store_content_fts.go`, change `offset := j * 5` to `offset := len(valueStrings) * 5` so placeholder `$N` numbering remains correct when items with empty `NodeID` are skipped
- [x] 3.2 Add a test case that calls `AppendContent` with a chunk where the first item has an empty `NodeID` and verify no SQL parameter error

## 4. UTF-8 Sanitization in AppendContent

- [x] 4.1 In `store_content_fts.go`, add `strings.ToValidUTF8(item.Body, "�")` when building `valueArgs` in `AppendContent`, sanitizing non-UTF-8 bytes before insertion into PostgreSQL
- [x] 4.2 Optional: add a `strings.Valid(body)` check with a warning log in `collectContentItems` (`content_split.go`) to surface which files produce non-UTF-8 content for diagnostics
- [x] 4.3 Verify the full indexing pipeline (`gortex daemon start --backend postgres`) completes without "invalid byte sequence for encoding UTF8" errors on a mixed-encoding corpus

## 5. Verification

- [x] 5.1 Run the existing conformance test suite against PG: `go test ./internal/graph/store_pg/ -v`
- [x] 5.2 Verify `go build ./cmd/gortex/` compiles cleanly
- [x] 5.3 Run a full index and search round-trip — TestIntegration_FullLifecycle + TestContentSearch_* PASS contra PG
/a