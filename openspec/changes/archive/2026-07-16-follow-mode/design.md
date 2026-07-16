# Design: follow-mode

## Context

Deployment topology this enables (kmcp): a **write plane** — one ephemeral indexer job (or singleton daemon) that clones repos, runs `gortex track --wait` per repo, indexes into a shared PostgreSQL schema, and exits — and a **read plane** — N stateless, diskless follower daemons serving MCP reads from that schema, horizontally scaled behind a load balancer, optionally against PG read replicas.

Facts established by code audit that shape this design:

- **Reads are already live.** With `--backend postgres`, `state.graph` *is* `*store_pg.Store`; queries hit SQL per call. There is no in-RAM graph to re-hydrate — a follower sees the writer's committed rows immediately. The original re-hydration-loop concept (shadow reload every 30s) is unnecessary.
- **`multiIndexer == nil` is the natural follower shape.** The MCP server already tolerates it: `sessionScope` returns unbound (unscoped store reads, `internal/mcp/server.go:1708-1741`), the dispatcher's `isCWDTracked` returns true (`cmd/gortex/daemon_mcp.go:285-287`) so cwd-carrying clients are not rejected, and `query.NewEngine` depends only on the store. A non-nil-but-empty MultiIndexer is the *wrong* shape: bound sessions get a match-nothing sentinel and the dispatcher rejects every `tools/call` with `repo_not_tracked`.
- **The warmup path is the eviction hazard.** `TrackRepoCtx` hard-fails on missing paths (`internal/indexer/multi.go:1592`), and `ReconcileRepoCtx`→`IncrementalReindex` classifies files "tracked in fileMtimes but absent from disk" as deleted and evicts them (`indexer.go:5035-5093`). Not constructing the MultiIndexer sidesteps all of it.
- **Residual writers that bypass tool presets** (from the boot/steady-state write inventory): `SetRepoIndexState` fires unconditionally per reconciled repo (`multi.go:1882` — moot with no MultiIndexer), `PrewarmCoChange` persists edges on first boot (`daemon.go:603`), `reconcileRationale` writes virtual graph nodes from `why`/memory tools with zero disk dependency (`internal/mcp/rationale_projection.go:112-114`), federation proxy hydration writes on traversal reads (`proxy_hydrate.go:53`), and `ensureFresh` self-heal reindexes stale files from read tools (`tools_enhancements.go:45`).
- **Preset enforcement is hide-mode-only** (`tools_mode.go:185`), and the control channel (`handleControl`, `internal/daemon/server.go:556-593`) is not gated by presets at all.
- **Document text is in the store.** Markdown prose sections are `KindDoc` without `data_class=content` — never leaned, full `section_text` persists in `nodes.meta` (JSONB). Content-class sections (pdf/text/pptx/xlsx) have full bodies in `content_fts` keyed by node + ordinal, with a 240-byte snippet on the node.

## Goals / Non-Goals

**Goals:**
- A diskless daemon serves the complete read-tool surface (graph, search, docs) from a shared PG schema, provably unable to write to it.
- A follower boots to `ready` in seconds against a populated schema and stays fresh with zero reload machinery.
- Vault/document content is readable without disk; disk-only tools fail with a typed, recoverable error.
- Exactly one indexing process can write a schema at a time.

**Non-Goals (v1+):**
- Workspace-scoped multi-tenant followers (roster-from-store). v0 followers serve unscoped reads; the schema is assumed single-workspace. Clients narrow with `repo:` args if needed.
- Derived-cache invalidation on writer publish (bundle cache, contract registry) and `graph_invalidated` push. Acceptable staleness for read-plane v0; the caches self-correct on restart.
- `LISTEN/NOTIFY`, writer failover/leader election.
- Byte-exact file reconstruction for docs (section-level fidelity, explicitly marked). Code files served from blobs ARE byte-exact (D7).
- Git-dependent tools on followers (diff/review/PR, blame/churn mining): these need a working tree and history, which is writer-plane by definition.

## Decisions

### D1 — Follow boot constructs no MultiIndexer, skips warmup entirely
`--follow` takes a separate boot branch in `buildDaemonState`/`runDaemonStart`: open store read-only → build engine + MCP server with `multiIndexer == nil` → publish `snapshot_loaded` then `ready` → serve. No parse phases, no `BackfillWorkspaceSlugs`, no contract rehydration pass (registry loads lazily from graph where supported), no watcher/janitor/snapshotter/enrichment/LSP. Rationale: every audited warmup writer is downstream of the MultiIndexer; not building it eliminates the class instead of guarding each member. Alternative rejected: empty-roster MultiIndexer + guards — the dispatcher and bound-session sentinels actively break clients in that shape, and the guard list becomes a maintenance treadmill.

### D2 — Validation: `--follow` requires `--backend postgres`; sqlite/memory refuse at flag parse
The design premise (live SQL reads, shared SOT) only holds for PG. Error message names the constraint.

### D3 — Three-layer write seal, plus four point gates
1. Preset: follow forces `readonly` + `hide` mode, overriding env/config (`GORTEX_TOOLS` cannot widen it).
2. Control channel: `handleControl` returns typed `follow_mode` errors for `ControlTrack`/`ControlUntrack`/`ControlReload`/`ControlProxy` when in follow.
3. Store: `Config.ReadOnly` guard (from `harden-pg-store`) as the backstop that also catches anything the audit missed.
Point gates on a server-level `followMode` flag: `reconcileRationale` becomes a no-op (memory tools still write the machine-local sidecar; only the graph projection is skipped), `PrewarmCoChange` skipped, proxy hydrator not wired, `ensureFresh` short-circuits. `post_review` and `feedback` are additionally denied in follow (external/FS side effects unfit for a serving replica).

### D4 — Doc reads: store fallback keyed on node kind, not on follow mode alone
`read_file` on a path whose disk read fails (or in follow mode, always): fetch `GetFileNodes(path)`, and if the file's nodes are doc-bearing (`KindDoc` sections / content-class), reconstruct — markdown from `meta.section_text` ordered by `StartLine` with blank-line joins; content-class from a new ordinal-ordered `ContentByFile(repoPrefix, path)` store query. Response carries `source: "store"` and a fidelity note. `get_symbol_source` for a doc node serves that node's own `section_text` (exact for its range). Code files are served byte-exact from `file_blobs` (D7); only when a blob is genuinely absent (pre-blob index, GC race) does the tool return the typed `follow_no_disk` error with a hint to re-run the writer or read from a local checkout. The fallback also benefits the normal daemon (file deleted between index and read). Doc reconstruction remains preferable to the blob for doc files only when no blob exists; when both exist, the blob wins (byte-exact).

### D7 — Code source blobs: content-addressed, populated at parse time, served through one seam
A new `file_blobs(content_hash TEXT PRIMARY KEY, body BYTEA, size INT)` table stores every indexed file's bytes, keyed by the `content_hash` the `files` table already records per `(repo_prefix, file_path)`. Content addressing deduplicates identical files across repos and re-indexes; PG TOAST compresses bodies transparently. The writer populates blobs in the per-file parse path (the worker already holds the bytes it just read to parse — the write is nearly free) and via COPY in the bulk path; blobs for hashes no longer referenced by any `files` row are garbage-collected after flush.

Reads route through one server-level helper `sourceFor(path|node)`: overlay → disk → store blob (`files` join `file_blobs`, then line-slice). `readLinesForCtx`/`resolveNodePath` callers — `read_file`, `get_symbol_source`, `batch_symbols`, `smart_context` excerpts, `get_editing_context` reads, `compress_bodies`/salience — inherit the fallback. `search_text` (trigram) and `search_ast` gain a `FileSource` abstraction (disk-backed today, blob-backed on followers): tree-sitter and the trigram builder consume bytes and don't care about the origin.

Fidelity/etag semantics on a follower are clean by construction: the blob IS the indexed version — exactly what the graph's line ranges describe — so `source:"store"` responses use `content_hash` as the etag, byte-exact. Alternative rejected: reconstructing code from symbol nodes (bodies aren't stored; partial output misleads). Alternative rejected: external object storage (S3) — a second infra dependency for data that is small (deduped, compressed source) and transactionally coupled to the index.

### D5 — Writer advisory lock is session-scoped and held for the daemon's life
Non-follow PG daemons acquire `pg_advisory_lock(<fixed key ⊕ schema>)` at boot (after pool open, before any write); failure to acquire within a short timeout aborts boot with a clear "another writer holds this schema" error. Session-scoped (not xact) so it lives exactly as long as the process and releases on crash via connection drop. The lock key incorporates the schema name so multiple schemas on one server don't false-conflict. Followers never take it.

### D6 — Status and health
`StatusResponse` gains `Mode` (`"follow"`) and `FreshnessLagSeconds` (now − max `repo_index_state.indexed_at`; `repo_index_state` is already readable without a roster). `/healthz` returns non-200 when the store health accessor reports persistent degraded reads, or when lag exceeds an optional `GORTEX_FOLLOW_MAX_LAG`. This is what k8s readiness/liveness probes consume.

## Risks / Trade-offs

- [Unscoped sessions expose the whole schema to any connected client] → v0 assumes a single-workspace schema per deployment (kmcp's shape); documented loudly. Multi-tenant scoping is the v1 roster-from-store work.
- [Follower serves mid-index states during writer runs (evict→reinsert windows)] → inherent to live SQL reads; `harden-pg-store` shrinks the windows (transactional merge with stale-delete), `repo_index_state.dirty` is exposed in status for clients that care, and full read-consistency gating is explicitly out of scope.
- [Nil-multiIndexer deref in rarely-exercised tools] → integration test boots a follower against a seeded schema and sweeps the registered read-tool surface asserting no panics; tools with hard roster dependencies must return typed errors, not crash.
- [Doc reconstruction is not byte-exact] → responses marked `source:"store"`; documented. Etag semantics: store-served reads use a content hash of the reconstruction, never the disk etag.
- [Writer lock adds a boot failure mode for existing single-daemon PG users] → only fails when a second writer actually contends — which is the accident the lock exists to stop; error message explains and names the holder PID via `pg_stat_activity`.
- [Contract registry and analysis caches on followers go stale between restarts] → accepted for v0 (read plane is primarily graph+vault); listed as v1 invalidation work.
- [`file_blobs` grows the schema and the bulk-load payload] → content addressing dedups identical files, TOAST compresses transparently, and blob GC prunes unreferenced hashes after flush; measure with the bulk perf assertion. Binary/oversized files respect the indexer's existing exclusion rules — only files that produced graph nodes get blobs.
- [Blob missing for a file the graph knows (schema indexed by a pre-blob writer)] → typed `follow_no_disk` error names the cause and the fix (re-run the writer); the integration test covers the mixed state.

## Migration Plan

Additive, flag-gated. Deploy order: (1) land `harden-pg-store` and roll the writer binary; (2) run one writer indexing cycle so the schema is LOGGED + current version; (3) start followers with `--follow`. Rollback: stop followers; the writer/schema are unaffected.

## Open Questions

- Should follow mode expose `save_note`/`store_memory` (machine-local sidecar writes, invisible to other replicas) or deny them for least surprise? Default: allow, document the locality.
- `GORTEX_FOLLOW_MAX_LAG` default: off (lag reported, never fatal) — confirm with kmcp SLOs.
- Whether `gortex daemon status` should also print writer-lock holder info on the writer (nice for ops, trivial via `pg_locks`).
