# Proposal: adaptive-embedding-dimensions

## Why

The PostgreSQL schema hardcodes the vector column dimension (`schema.go:173`: `vec vector(50) NOT NULL`) while the embedding provider is configurable (`--embeddings-url` supports any OpenAI-compatible/Ollama backend). Any provider whose dimensionality differs from the baked-in value makes **every vector upsert fail** with `SQLSTATE 22000: expected 50 dimensions, not 1536` — observed in production (2026-07-17) with OpenAI `text-embedding-3-small` (1536 dims): the structural graph indexes fine while the entire semantic corpus is silently lost (the error only surfaces in daemon stderr). The in-process model, API models (384/768/1536), and reduced-dimension requests all have different dimensionalities; the schema must follow the provider, not the other way around.

## What Changes

- **Dimension discovery at daemon startup**: probe the active embedding provider once (embed a sentinel string, measure the vector length) before the vector store is used. The probe result is the source of truth for the column dimension.
- **Schema creation follows the probe**: on a virgin store, `vectors.vec` is created as `vector(N)` with the probed dimension; the HNSW index (already parameterized via `BuildVectorIndex(dims int)`) uses the same value.
- **Embedding-space metadata persisted**: a metadata record (provider identity, model name, dims) is written on first initialization and validated on every subsequent boot.
- **Fail-fast on mismatch**: if the probed dimension (or provider/model identity) differs from the persisted metadata, the daemon refuses to start vector operations with an actionable error naming both spaces and pointing to the explicit reset path. No silent mixing, no automatic migration.
- **Explicit reset path** (`--embeddings-reset` or equivalent): drops vector data + metadata and recreates the column for the new space. Structural graph data is untouched.
- **Optional dimension override** (`GORTEX_EMBEDDINGS_DIMS` / flag): skips the probe (air-gapped migrations) and, for providers that support requested dimensionality (OpenAI `text-embedding-3-*` `dimensions` parameter), is also sent with each embedding request — one knob drives both the column and the API.

## Capabilities

### New Capabilities
- `embedding-space-contract`: discovery, persistence, and validation of the embedding space (provider, model, dimensions) that the vector store is bound to, including the explicit reset path and the optional dimension override.

### Modified Capabilities
<!-- none: pg-bulk-load and follow-daemon requirements are unaffected; the vectors table DDL is an implementation detail of the new contract -->

## Impact

- **Code**: `internal/graph/store_pg/schema.go` (vectors DDL no longer hardcodes 50), `internal/graph/store_pg/store_vector.go` (comment drift says 384; wire dims through creation), embedding provider interface (probe + optional requested-dims), daemon startup sequence (probe → validate → serve), CLI (`--embeddings-reset`, dims flag/env).
- **Operational**: existing deployments with populated `vector(50)` columns and a non-50-dim provider are already broken (all upserts fail); after upgrade they will fail fast at startup with a clear message instead — the reset path re-embeds. Followers must query with the same provider/model; the persisted metadata gives them a way to verify.
- **Docs**: `docs/` embeddings section — provider switch procedure (reset + re-embed).
