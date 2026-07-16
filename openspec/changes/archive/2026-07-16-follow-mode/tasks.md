# Tasks: follow-mode

> Depends on `harden-pg-store` (read-only open mode `Config.ReadOnly`, non-panicking reads, pool timeouts). Do not start sections 2-5 before its sections 2 and 3 land.

## 1. Writer advisory lock (independent of follow boot; can land first)

- [x] 1.1 Add `AcquireWriterLock(ctx)` to `store_pg`: `pg_advisory_lock` on a fixed key ⊕ schema hash over a dedicated pooled connection held for the store's life; bounded acquire timeout returning a typed conflict error (include holder PID from `pg_stat_activity` when queryable)
- [x] 1.2 Call it from the non-follow PG daemon boot path after pool open, before any warmup write; abort boot on conflict with the writer-conflict message
- [x] 1.3 Tests: second writer refused while first holds; lock released on close/crash (drop the connection); two schemas on one server don't contend; follow mode never acquires

## 2. Follow boot path

- [x] 2.1 Add `--follow` flag to `gortex daemon start`; validate `--backend postgres` (typed refusal otherwise); plumb a `followMode` bool through daemon state and MCP server construction
- [x] 2.2 Follow boot branch in `buildDaemonState`/`runDaemonStart`: open store with `Config.ReadOnly: true`, skip MultiIndexer construction, skip warmup goroutine entirely, publish `snapshot_loaded` then `ready`, skip snapshotter/janitor/watcher/enrichment/LSP/PrewarmCoChange wiring
- [x] 2.3 Verify server construction with `multiIndexer == nil` end-to-end: engine from store, `RunAnalysis` allowed (in-memory), readiness broadcaster, HTTP mount (`--http-addr`) working in follow
- [x] 2.4 Status: add `Mode` and `FreshnessLagSeconds` (max `repo_index_state.indexed_at`) to `StatusResponse` and render in `gortex daemon status` (table + TUI + JSON)
- [x] 2.5 `/healthz`: non-200 on store health degradation; optional `GORTEX_FOLLOW_MAX_LAG` threshold (default off)

## 3. Write seal

- [x] 3.1 Force preset `readonly` + `hide` mode when `followMode` (override `GORTEX_TOOLS`/config; log the override once)
- [x] 3.2 Deny `post_review` and `feedback` in follow (extend the follow deny set on top of `daemon.MutatingTools`)
- [x] 3.3 Control channel: `handleControl` returns typed `follow_mode` error for `ControlTrack`/`ControlUntrack`/`ControlReload`/`ControlProxy` when in follow
- [x] 3.4 Point gates on `followMode`: `reconcileRationale` no-op (sidecar memory writes still allowed), federation proxy hydrator not wired, `ensureFresh` short-circuit
- [x] 3.5 Tests: each layer independently — preset blocks editors, control RPCs refuse, rationale projection skipped on `why`/memory tools, store guard trips recorded in health when bypassed artificially

## 4. Store-backed doc reads

- [x] 4.1 Store: add ordinal-ordered `ContentByFile(repoPrefix, filePath)` to the PG content capability (and the sqlite equivalent for the deleted-file fallback parity)
- [x] 4.2 Doc reconstruction helper: given a file's nodes, classify (markdown `section_text` vs content-class) and reconstruct in `StartLine`/ordinal order; compute store etag from reconstructed bytes
- [x] 4.3 `read_file`: on disk-read failure or follow mode, attempt doc reconstruction; mark `source:"store"`; non-doc files return typed `follow_no_disk` error with local-checkout hint
- [x] 4.4 `get_symbol_source`: doc nodes serve own `section_text` (content-class fetch full body via `ContentByFile`) when disk unavailable/follow; mark `source:"store"`
- [x] 4.5 Typed `follow_no_disk` error for git-dependent tools (diff/review/PR, blame/churn/coverage/release mining) in follow mode
- [x] 4.6 Tests: markdown round-trip (index fixture → delete file → read from store), content-class ordinal ordering, etag stability across identical reconstructions

## 5. Code source blobs

- [x] 5.1 Schema migration: `file_blobs(content_hash TEXT PRIMARY KEY, body BYTEA NOT NULL, size INTEGER NOT NULL)`; document TOAST compression expectations
- [x] 5.2 Store API: `PutFileBlobs(items)` (dedup on conflict), `GetFileBlobByPath(repoPrefix, filePath)` (join via `files.content_hash`), `GetFileBlobByHash(hash)`; wire `PutFileBlobs` into the bulk-load COPY path
- [x] 5.3 Writer populate: in the per-file parse path (bytes already in hand), enqueue the blob alongside the `files` row; respect existing exclusion rules (only files that produce graph nodes)
- [x] 5.4 Blob GC after flush: delete `file_blobs` rows whose hash no longer appears in `files`
- [x] 5.5 Server `sourceFor(path|node)` helper: overlay → disk → blob; route `readLinesForCtx`/`resolveNodePath` consumers through it (`read_file`, `get_symbol_source`, `batch_symbols`, `smart_context` excerpts, `get_editing_context` reads, `compress_bodies`/salience); blob-served responses marked `source:"store"` with content-hash etag
- [x] 5.6 `FileSource` abstraction for `internal/search/trigram` and the AST search: disk-backed (current behavior) and blob-backed implementations; follower wires blob-backed so `search_text`/`search_ast` work diskless
- [x] 5.7 Tests: dedup (two repos, one blob), GC on re-index, byte-exact `get_symbol_source` from blob vs disk, `smart_context` excerpts on a diskless server, trigram/AST parity disk vs blob over identical content, typed error when blob absent (pre-blob schema)
- [x] 5.8 Measure bulk-load overhead with blobs enabled (`GORTEX_BULK_PERF_ASSERT`) and record schema size delta on a representative workspace

## 6. Integration + verify

- [x] 6.1 Integration test: seed a PG schema (writer path), boot a follow daemon against it with no repos on disk, sweep every registered read tool asserting no panic and no store write (assert write-guard health counter stays zero)
- [x] 6.2 Freshness test: writer commits new rows while follower runs; follower queries observe them without restart
- [x] 6.3 Full-surface test on the follower: `smart_context`, `read_file` (code + markdown), `get_symbol_source` (code + doc), `search_text` regex, `search_ast` all serve from the DB with `source:"store"` where applicable
- [x] 6.4 `go test -race ./...` green
- [x] 6.5 Manual: two-machine (or two-process) run — writer job (`daemon start` + `track --wait` + stop) then follower serving vault markdown AND code source; document the runbook in `docs/pg-setup.md` (deploy order, read-replica caveats, pgbouncer DSN)
- [x] 6.6 Update CLAUDE.md / docs with follow-mode usage and the kmcp reference topology (writer-as-job + N followers)
