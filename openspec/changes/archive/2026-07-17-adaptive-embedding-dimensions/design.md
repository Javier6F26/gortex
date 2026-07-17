# Design: adaptive-embedding-dimensions

## Context

The vector store DDL bakes in `vector(50)` (`internal/graph/store_pg/schema.go:173`) while the embedding provider is runtime-configurable: in-process model, Ollama (`nomic-embed-text`: 768, `embeddinggemma`: 768), OpenAI (`text-embedding-3-small`: 1536, or any requested dimension via the `dimensions` request parameter). `store_vector.go` already threads `dims` through `BuildVectorIndex(dims int)` and stores a `dims` column per row — only the column type itself is fixed. pgvector enforces the declared dimension on insert, so any provider ≠ 50 dims fails every upsert with SQLSTATE 22000 while the rest of indexing proceeds — a silent semantic-corpus loss unless daemon stderr is watched.

Incident (2026-07-17, albatros-intelligence): full 300-repo cold index against OpenAI 1536-dim embeddings produced a complete structural graph and zero usable vectors.

## Goals / Non-Goals

**Goals:**
- The vector column dimension always matches the active embedding provider.
- Provider/model/dimension changes are detected and refused loudly at startup — never silent vector loss or mixed spaces.
- Operators switch providers via one explicit, documented reset action.
- Support providers with client-selectable dimensionality (OpenAI `dimensions` param) through the same knob.

**Non-Goals:**
- Automatic re-embedding on provider change (explicitly operator-triggered).
- Multi-space storage (several embedding models side by side) — one space per schema.
- Backfilling vectors lost by the current bug (operational task, not code).

## Prior art in the codebase (reduces scope)

The probe is **already built and wired**, discovered while validating this proposal:

- `internal/embedding/api.go:177` — `APIProvider.ProbeDimensions(ctx) (int, error)`: embeds a sentinel, caches and returns the width. Handles provider-unreachable (returns error, leaves dims 0), doubles as a connectivity/credential check.
- `internal/embedding/api.go:158` — `APIProvider.Dimensions() int`.
- `internal/serverstack/shared_server.go:485-525` — the probe is **already called at daemon startup** and the result stored as `s.EmbedderDims`. Its own doc-comment describes the exact incident (the `daemon_state.go:173` `vec.Dims == EmbedderDims` snapshot gate).

So task 1.1/1.2 are largely done. The remaining work is to route `EmbedderDims` into the **column DDL**, which is today static. `BuildVectorIndex(dims int)` only validates `dims > 0` (`store_vector.go:26`) — it does **not** size the index from `dims`; the real parameterization point is the column type, not the index.

The `vectors` table is **ephemeral**: migration V2 (`schema_version.go:32`) records "Vectors are ephemeral (rebuilt each index run via BulkUpsertEmbeddings), so dropping and recreating is clean," and that migration already does `DROP TABLE vectors; CREATE ...`. Therefore the reset is not a destructive data operation — vectors are rebuilt on any re-index anyway. The reset's real purpose is (a) resize the column to the new space and (b) prevent silent space-mixing and surprise re-embed cost, not preventing data loss.

**Out of scope — SQLite backend unaffected.** The SQLite store keeps vectors as untyped blobs (`store_sqlite/store_vector.go:36`, "only validates/records intent") with no pgvector dimension enforcement, so it cannot hit SQLSTATE 22000. This contract is PostgreSQL-only by construction.

## Decisions

### D1 — Probe at startup as the source of truth (not config)

On daemon start with embeddings enabled, embed a sentinel string and measure `len(vector)`. Dimensions are a property of the model, not operator knowledge — asking humans to supply 384/768/1536 invites the same failure class, silently when the guessed number happens to be a valid but wrong dimension. Reuse the existing `ProbeDimensions`/`EmbedderDims` path rather than adding a second probe.

- Alternative rejected — env-only: misconfiguration reproduces the incident; correct values are discoverable automatically.
- Probe cost: one embedding call at boot. Failure to probe (provider unreachable) blocks vector-store readiness the same way the provider being down blocks embedding: fail with a clear error (structural indexing may proceed; see D5).

### D2 — Embedding-space metadata table, validated every boot

New table (e.g. `embedding_space(provider TEXT, model TEXT, dims INT, created_at ...)`, single row). Written on first initialization; every boot compares the probe result against it.

- Match → serve.
- Mismatch → refuse vector operations with an error naming stored vs. probed space and the reset command. The stored identity also lets followers verify they query with the same space.

### D3 — Column created at first use, not in static DDL

`vectors.vec` DDL moves out of the static schema block; the table is created (or altered on reset) with `vector(N)` once N is known (probe or override). `BuildVectorIndex(dims)` keeps its signature — callers pass the metadata dims.

- Alternative rejected — `vector` without dimension (pgvector allows untyped): loses insert-time validation and HNSW requires typed columns for index builds; keeping the type is the guardrail.

### D4 — Explicit reset (`--embeddings-reset`)

Drops `vectors` + `embedding_space`, recreates with the new probe. Structural tables untouched. Refuses to run while another writer holds the schema lock.

### D5 — Optional override `GORTEX_EMBEDDINGS_DIMS` / `--embeddings-dims`

When set: skips the probe (air-gapped), sizes the column, and — for OpenAI-compatible providers — is sent as the `dimensions` request parameter so requested and stored dimensionality can never diverge. Validation: if the provider returns vectors of a different length than the override, fail the batch loudly (defense against providers that ignore the parameter).

### D6 — Sequencing: `vectors` DDL leaves the static migration chain

Today `ensureSchema()` (via the static V1/V2 migration chain) creates `vectors(50)` during `store.Open()` — **before** the embedder probe runs in `shared_server.go`. The dynamic column cannot be born in the static chain. Plan:

- Remove the `vectors` DDL from the static schema/migration chain; the chain still owns `embedding_space` and everything structural.
- After the probe resolves the dimension (or the override is set), a dedicated `EnsureVectorTable(dims)` step creates `vectors vector(N)` idempotently and writes/validates `embedding_space`.
- **Invariant:** no code path may touch `vectors` between `Open()` and `EnsureVectorTable`. The writer's vector-store readiness gate must depend on `EnsureVectorTable` having run, not merely on `Open()`.
- Legacy stores that already have `vectors(50)` from the old chain keep it; `EnsureVectorTable` sees an existing typed column and synthesizes `embedding_space` from `atttypmod` (D2/migration plan) instead of recreating.

### D7 — Follower on space mismatch: degrade to BM25, do not fail queries

A follower embeds the query at request time. If its configured provider/model yields a different width than the stored `embedding_space`, pgvector's `<=>` rejects the query vector outright — a **hard query error**, not merely searching a foreign space. Therefore the follower, on detecting a mismatch at boot, SHALL disable semantic vector search and degrade to BM25, logging a warning — never surface pgvector dimension errors to callers. (Supersedes the earlier "warn-only in follower" recommendation.)

## Risks / Trade-offs

- **[Probe adds a provider dependency at boot]** → Same dependency already exists for indexing; a daemon that cannot embed cannot maintain the semantic corpus anyway. Structural serving is unaffected.
- **[Existing broken deployments (50-dim column, ≠50 provider)]** → After upgrade they fail fast at startup instead of silently losing vectors — strictly better; the error message includes the reset instruction.
- **[Reset drops the whole semantic corpus]** → Intended: cross-space vectors are unusable. The message must state re-embed cost clearly.
- **[Comment drift]** → `store_vector.go:15` claims `vector(384)`, schema says 50 — both wrong post-change; sweep comments in the same PR.

## Migration Plan

1. Ship schema-version bump: on upgrade, daemons with an existing `vectors` table and no `embedding_space` row synthesize metadata from the column's declared dimension (readable from `pg_attribute`/`atttypmod`) and provider config — preserving working 50-dim in-process deployments without a reset.
2. Deployments already mismatched fail fast with the reset instruction.
3. Rollback: previous binary ignores the metadata table; no destructive change until an operator runs the reset.

## Open Questions

- Follower behavior on space mismatch — **resolved (D7): degrade to BM25 + warn**, refuse in writer.
- Sentinel string for the probe: reuse the existing constant `"gortex embedding dimension probe"` from `api.go:183` (deterministic, cacheable upstream).
