# Tasks: adaptive-embedding-dimensions

## 1. Embedding-space discovery

- [x] 1.1 REUSE existing `ProbeDimensions`/`Dimensions()` (`api.go:177/158`) — extend to any providers missing it (gomlx/hugot/onnx already expose `Dimensions()`); do not add a second probe
- [x] 1.2 The probe already runs at startup (`shared_server.go:485-525` → `s.EmbedderDims`); route `EmbedderDims` into vector-store init (see 2.1). Provider unreachable → clear error, structural indexing unaffected
- [x] 1.3 `--embeddings-dims` flag + `GORTEX_EMBEDDINGS_DIMS` env: overrides the probe; forwarded as `dimensions` request param on OpenAI-compatible providers
- [x] 1.4 Batch-time guard: returned vector length ≠ contract dims → fail the batch with an explicit dimension-contract error

## 2. Store: dynamic column + metadata

- [x] 2.1 Remove the `vectors` DDL from the static schema/migration chain (`schema.go` + V1/V2 in `schema_version.go`); add an idempotent `EnsureVectorTable(dims)` that runs AFTER the probe (D6) and creates `vectors vector(N)`. Invariant: no path touches `vectors` between `Open()` and `EnsureVectorTable`; writer vector readiness gates on it
- [x] 2.2 Add `embedding_space` metadata table (provider, model, dims, created_at); write on first init
- [x] 2.3 Startup validation: probe/override vs. stored metadata; mismatch → fail fast naming both spaces + reset command
- [x] 2.4 Legacy migration: existing typed column without metadata → synthesize metadata from `atttypmod` + configured provider
- [x] 2.5 Fix comment drift (`store_vector.go:15` says 384; schema said 50)

## 3. Reset path

- [x] 3.1 `gortex embeddings reset` (or `daemon start --embeddings-reset`): drop `vectors` + `embedding_space`, reinitialize for the configured provider; refuse while another writer holds the schema lock
- [x] 3.2 Error message for mismatch includes the exact reset invocation and the re-embed cost warning

## 4. Follower behavior

- [x] 4.1 Follower reads `embedding_space` at boot; on mismatch it DEGRADES semantic search to BM25 and logs a warning naming both spaces (D7) — never surfaces a pgvector dimension error, never refuses to start

## 5. Tests & docs

- [x] 5.1 Tests: virgin init per provider dims (50/768/1536); mismatch fail-fast; override + ignored-override guard; legacy metadata synthesis; reset preserves structural tables
- [x] 5.2 Docs: embeddings section — provider switch procedure (reset + re-embed), override semantics

<!-- Two unrelated bugs surfaced in the same 2026-07-17 incident were extracted
     to their own changes so they stay actionable:
       - fix-staging-merge-qualname-dup (idx_nodes_qual_name 23505)
       - fix-indexer-session-memory-accumulation (in-session RSS climb → OOM) -->
