# Proposal: follow-mode

## Why

kmcp (albatros-intelligence) needs always-available graph and vault reads from N stateless replicas without shipping repo checkouts to every replica. Today a gortex daemon fuses three roles — clone/track, index, serve — so scaling reads means scaling disk, git access, and indexing cost together, and pointing a vanilla daemon at a shared PG schema without repos on disk is actively destructive (its warmup reconcile evicts "missing" files from the shared schema). Code audit confirmed the enabling fact: with the PostgreSQL backend, node/edge/symbol-search/content/vector queries are already served live from SQL — a read-only daemon needs no re-hydration loop to stay fresh, only a safe boot path and a sealed write surface.

## What Changes

- **`gortex daemon start --follow`** (requires `--backend postgres`): boots without a MultiIndexer (no repo tracking, no `os.Stat` on repo paths, no warmup indexing, no watchers, no reconcile janitor, no enrichment/embedder, no LSP), opens the store in read-only mode (`harden-pg-store`'s `Config.ReadOnly`), publishes readiness directly (`snapshot_loaded → ready`), and serves the full read-tool surface from the shared store.
- **Sealed write surface, three layers**: (1) tool preset forced to `readonly` in `hide` mode (the preset gate is inert in defer mode); (2) control-channel RPCs `track`/`untrack`/`reload` return a typed `follow_mode` error; (3) the read-only store guard from `harden-pg-store` as the final backstop. Point gates disable the residual writers that bypass presets: rationale projection (`why`/memory tools write virtual graph nodes), co-change prewarm at boot, federation proxy hydration, and the `ensureFresh` read-path self-heal.
- **Store-backed document reads**: `read_file` and `get_symbol_source` serve markdown/doc content from the graph store when disk is unavailable — markdown sections from `nodes.meta.section_text` ordered by line range, content-class sections (pdf/text/pptx/xlsx) from `content_fts.body` by ordinal — responses marked `source: "store"`.
- **Code source blobs**: the writer persists every indexed file's bytes into a new content-addressed `file_blobs` table (keyed by the `content_hash` the `files` table already carries, deduplicated across repos), and the server's source-read seam falls back disk → store blob. This completes the read surface on diskless followers: `read_file`/`get_symbol_source` on code, `smart_context` source excerpts, `batch_symbols`, `compress_bodies`, and — via a `FileSource` abstraction — `search_text` (trigram) and `search_ast`. Only git-dependent tools (diff/review/PR, blame/churn mining) remain writer-side, returning a typed, recoverable `follow_no_disk` error.
- **Writer advisory lock**: a daemon that indexes (non-follow, PG backend) takes a PostgreSQL advisory lock at boot, so two writers — or an accidental second normal daemon pointed at the shared DSN — cannot interleave writes.
- **Operability**: `gortex daemon status` reports `mode: follow` and freshness lag (max `repo_index_state.indexed_at` vs. now); `/healthz` (when HTTP is enabled) reflects store health (degraded reads, write-guard trips) and optional max-lag.

## Capabilities

### New Capabilities
- `follow-daemon`: the read-only follower daemon lifecycle — boot without repos, sealed write surface, readiness, status/lag reporting.
- `store-backed-doc-reads`: serving document content (markdown + content-class sections) from the graph store instead of disk, with explicit source marking and typed errors for disk-only tools.
- `pg-writer-lock`: single-writer enforcement via PostgreSQL advisory lock for indexing daemons on the PG backend.
- `code-source-blobs`: content-addressed storage of indexed file bytes in the graph store, and the disk→store fallback in the server's source-read seam that serves code source without a filesystem.

### Modified Capabilities
<!-- none — existing capabilities keep their requirements; follow mode is additive behind a flag -->

## Impact

- Affected code: `cmd/gortex/daemon.go` / `daemon_state.go` (follow boot branch, skip warmup/watchers/janitor, writer lock), `internal/daemon/server.go` (control-channel gate), `internal/mcp` (preset forcing, point gates in `rationale_projection.go`, `tools_cochange.go`, `proxy_hydrate.go`, `tools_enhancements.go` (`ensureFresh`), doc read path in `tools_fileops.go`/`tools_coding.go`), `internal/graph/store_pg` (content-by-file ordinal query), `internal/daemon/proto.go` (status fields).
- Depends on `harden-pg-store` (read-only open mode, non-panicking reads, timeouts). Blocked until its tasks 2.x and 3.x land.
- No breaking changes: follow mode is opt-in behind `--follow`; normal daemon behavior is unchanged except the (new) writer advisory lock on the PG backend.
- Additional affected code for blobs: `internal/graph/store_pg/schema.go` (+`file_blobs` table, V-next migration), a blob-populate step in the indexer's per-file path and bulk load, the server source-read helper, and `internal/search/trigram` / AST search gaining a `FileSource` abstraction.
- Deferred to v1 (out of scope): roster-from-store for workspace-scoped multi-tenant followers, cache-invalidation loop + `graph_invalidated` broadcast on writer publishes, `LISTEN/NOTIFY`.
