## 1. Scaffold & Dependencies

- [x] 1.1 Create `internal/graph/store_pg/` package directory structure
- [x] 1.2 Add `github.com/jackc/pgx/v5` and `github.com/pgvector/pgvector-go` to go.mod
- [x] 1.3 Write `store_pg/config.go` — DSN parsing, pgxpool config, pool sizing (`NumCPU * 2`)
- [x] 1.4 Write `store_pg/schema.go` — full DDL for PostgreSQL (nodes, edges, sidecars, indexes, extensions)
- [x] 1.5 Write `store_pg/schema_version.go` — inline schema versioning (table + read/write helpers)
- [x] 1.6 Write `store_pg/meta_json.go` — reuse JSON codec from store_sqlite (no changes, just import)

## 2. Core Store — Open, Close, CRUD

- [x] 2.1 Write `store_pg/store.go` — `Store` struct, `Open()` with schema migration + pool init, `Close()`, `ResolveMutex()`
- [x] 2.2 Write node operations: `AddNode`, `AddBatch` (nodes), `GetNode`, `GetNodeByQualName` — using pgx `ON CONFLICT DO UPDATE`
- [x] 2.3 Write edge operations: `AddEdge`, `AddBatch` (edges), `RemoveEdge`, `ReindexEdge` — using `ON CONFLICT DO NOTHING`
- [x] 2.4 Write batched edge mutations: `ReindexEdges`, `SetEdgeProvenance`, `SetEdgeProvenanceBatch`, `PersistEdgeAttributes`, `PersistEdgeAttributesBatch`
- [x] 2.5 Write eviction: `EvictFile`, `EvictRepo` — delete edges touching node IDs then delete nodes
- [x] 2.6 Write scan helpers: `scanNode`, `scanEdge`, `scanEdgeLight`, `queryNodesSQL`, `queryEdgesSQL` with pgx row scanning
- [x] 2.7 Write batch lookups: `GetNodesByIDs`, `FindNodesByNames`, `GetNodesByQualNames` — chunked IN-list queries with `= ANY($1)`

## 3. Edge Queries & Counts

- [x] 3.1 Write out-edge queries: `GetOutEdges`, `GetOutEdgesLight`, `GetOutEdgesForNodes`, `GetOutEdgesByNodeIDs`
- [x] 3.2 Write in-edge queries: `GetInEdges`, `GetInEdgesByNodeIDs`
- [x] 3.3 Write iteration queries: `AllNodes`, `AllEdges`, `GetRepoEdges`, `FindNodesByName`, `FindNodesByNameInRepo`, `FindNodesByNameContaining`
- [x] 3.4 Write file/repo scoped queries: `GetFileNodes`, `GetRepoNodes`, `RepoPrefixes`
- [x] 3.5 Write counts and stats: `NodeCount`, `EdgeCount`, `Stats`, `RepoStats`, `RepoMemoryEstimate`, `AllRepoMemoryEstimates`
- [x] 3.6 Write provenance verification: `EdgeIdentityRevisions`, `VerifyEdgeIdentities`
- [x] 3.7 Write predicate-shaped iterators: `EdgesByKind`, `NodesByKind`, `EdgesWithUnresolvedTarget`

## 4. Full-Text Search — Symbol Names (pg_trgm)

- [x] 4.1 Implement `UpsertSymbolFTS` — per-symbol upsert of tokenized name into nodes (indexed by pg_trgm GIN)
- [x] 4.2 Implement `BulkUpsertSymbolFTS` — bulk replace per-repo symbol corpus
- [x] 4.3 Implement `BuildSymbolIndex` — ensure pg_trgm GIN index exists (idempotent)
- [x] 4.4 Implement `SearchSymbols` — pg_trgm similarity query with name-based exact-match tier-0 short-circuit
- [x] 4.5 Implement `SearchSymbolBundles` — SearchSymbols + GetNodesByIDs + GetInEdgesByNodeIDs + GetOutEdgesByNodeIDs in minimal round-trips
- [x] 4.6 Implement `SetBundleFingerprints` — fingerprint cache for bundle staleness

## 5. Full-Text Search — Content Bodies (tsvector)

- [x] 5.1 Implement `WipeContent` — DELETE scoped to repo_prefix
- [x] 5.2 Implement `WipeContentFile` — DELETE scoped to file_path
- [x] 5.3 Implement `AppendContent` — INSERT content rows with tsvector generation
- [x] 5.4 Implement `SearchContent` — ts_query with ts_rank ordering and snippet via ts_headline
- [x] 5.5 Implement `BuildContentIndex` — ensure GIN index on tsvector column exists
- [x] 5.6 Implement `ScanContent` — stream every stored content row to consumer callback

## 6. Vector Search (pgvector)

- [x] 6.1 Implement `BuildVectorIndex` — ensure pgvector extension + HNSW index exist for given dims
- [x] 6.2 Implement `UpsertEmbedding` — INSERT ON CONFLICT DO UPDATE for single vector
- [x] 6.3 Implement `BulkUpsertEmbeddings` — bulk upsert vectors per repo
- [x] 6.4 Implement `SimilarTo` — ANN query with `vec <=> $1` ORDER BY, LIMIT
- [x] 6.5 Implement `GetEmbeddings` — batch read vectors by node IDs

## 7. Graph Traversal (Recursive CTEs)

- [x] 7.1 Implement `BFS` — recursive CTE with forward/backward direction, kind filter, depth bound, limit
- [x] 7.2 Implement `ReachableForwardByKinds` — layer-by-layer walk over outgoing edges
- [x] 7.3 Implement `ClassHierarchyTraverse` — up/down inheritance walk via recursive CTE
- [x] 7.4 Implement `ExpandFrontier` — batched edge+neighbor fetch for given frontier IDs
- [x] 7.5 Implement `FileEditingContext` — file node + defines + imports + calledBy + calls in few round-trips
- [x] 7.6 Implement `GetFileSubGraph` / `GetFileSubGraphCounts` — full file neighbourhood in one round-trip

## 8. Aggregators & Scanners

- [x] 8.1 Implement edge aggregators: `InEdgeCountsByKind`, `EdgeKindCounts`, `InDegreeForNodes`, `CrossRepoEdgeCounts`, `FileImportCounts`
- [x] 8.2 Implement node aggregators: `NodeIDsByKinds`, `NodesByKinds`, `NodeDegreeByKinds`, `NodesInFilesByKind`, `NodesByKindsScanner`
- [x] 8.3 Implement degree/fan aggregators: `NodeDegreeCounts`, `NodeFanCounts`, `EdgeAdjacencyForKinds`, `CommunityCrossingsByKind`
- [x] 8.4 Implement file/import aggregators: `FileImporters`, `FileSymbolNamesByPaths`, `EdgesByKindsScanner`, `ExternalCallCandidateEdges`
- [x] 8.5 Implement analysis scanners: `DeadCodeCandidates`, `IfaceImplementsRows`, `ExtractCandidates`, `CrossRepoCandidates`, `ThrowerErrorSurface`
- [x] 8.6 Implement structural scanners: `MemberMethodsByType`, `StructuralParentEdges`

## 9. Sidecars

- [x] 9.1 File mtimes: `BulkSetFileMtimes`, `ReplaceFileMtimes`, `DeleteFileMtimes`, `LoadFileMtimes`
- [x] 9.2 Ref facts: `BulkSetRefFacts`, `DeleteRefFactsByFiles`, `LoadRefFactsByFiles`, `LoadRefFactsByTargets`
- [x] 9.3 Clone shingles: `BulkSetCloneShingles`, `DeleteCloneShingles`, `LoadCloneShingles`
- [x] 9.4 Constant values: `BulkSetConstantValues`, `DeleteConstantValuesByFiles`, `ConstantValuesByNodeIDs`
- [x] 9.5 Enrichment state: `GetEnrichmentState`, `SetEnrichmentState`
- [x] 9.6 File metadata: `SetFileMetas`, `DeleteFileMetasByFiles`, `FileMetasForRepo`
- [x] 9.7 Enrichment sidecars: churn, coverage, release, blame (BulkSet + Delete + read queries)
- [x] 9.8 Index state: read/write repo_index_state
- [x] 9.9 DB stats: implement `store_dbstat.go` equivalent (PostgreSQL `pg_stat_user_tables` or pg_total_relation_size)

## 10. Bulk Load

- [x] 10.1 Implement `BeginBulkLoad` — create UNLOGGED staging tables, disable synchronous_commit
- [x] 10.2 Implement inner buffering — batch rows in Go buffers during bulk mode
- [x] 10.3 Implement `FlushBulk` — COPY FROM into staging, CREATE INDEX, ALTER TABLE RENAME, DROP old, restore settings
- [x] 10.4 Implement `BulkUpsertSymbolFTS` — bulk symbol FTS replacement during bulk load
- [x] 10.5 Implement `BulkUpsertEmbeddings` — bulk vector replacement during bulk load

## 11. Backend Resolver (Bulk Server-Side Resolution)

- [x] 11.1 Implement `ResolveSameFile` — server-side join for same-file symbol resolution
- [x] 11.2 Implement `ResolveSamePackage` — server-side join for same-package resolution
- [x] 11.3 Implement `ResolveImportAware` — join against EdgeImports adjacency
- [x] 11.4 Implement `ResolveRelativeImports` — Python/Dart relative import stubs
- [x] 11.5 Implement `ResolveCrossRepo` — cross-repo unique name resolution
- [x] 11.6 Implement `ResolveUniqueNames` — global unique name resolution
- [x] 11.7 Implement `ResolveExternalCallStubs` — external:: node synthesis
- [x] 11.8 Implement `ResolveAllBulk` — chain all resolver rules in precision order

## 12. Wiring — CLI Flags & Backend Selection

- [x] 12.1 Add `case "postgres"` / `case "pg"` to `OpenBackend()` in `serverstack/backend.go`
- [x] 12.2 Add `openPostgresBackend()` function — creates pgxpool, opens store_pg.Store
- [x] 12.3 Add `isEmbeddedBackend()` to replace `isSqliteBackend()` — gates flock only for file-based backends
- [x] 12.4 Add `--pg-dsn` and `--pg-pool-size` flags to `gortex daemon start` in `cmd/gortex/daemon.go`
- [x] 12.5 Update `daemonState` to pass pg DSN to `NewSharedServer`
- [x] 12.6 Gate flock in `shared_server.go` behind `isEmbeddedBackend()` instead of `isSqliteBackend()`
- [x] 12.7 Update `normalizeBackendTag()` in `internal/daemon/paths.go` for `"postgres"`
- [x] 12.8 Update `cmd/gortex/repos_cmd.go` to support reading index state from PostgreSQL (via DSN or daemon IPC)
- [x] 12.9 Keep temp SQLite for init (one-shot ephemeral store, no benefit to PostgreSQL) — keep temp SQLite or allow PostgreSQL init

## 13. Tests

- [x] 13.1 Write `store_pg/store_test.go` — call `storetest.RunConformance(t, factory)` and pass
- [x] 13.2 Write store_pg unit tests for Open/Close, schema migration, pool lifecycle
- [x] 13.3 Write FTS tests: symbol search edge cases (empty, unicode, special chars), bundle search
- [x] 13.4 Write content search tests: tsvector matching, snippets, per-repo scope, scan
- [x] 13.5 Write vector search tests: SimilarTo accuracy, HNSW build, bulk upsert, dimension mismatch error
- [x] 13.6 BFS tests pass via conformance suite: forward/backward, depth limits, kind filters, cycles, determinism
- [x] 13.7 Aggregator tests pass via conformance suite: each aggregator against known data
- [x] 13.8 Sidecar tests pass via conformance suite: read/write/replace/delete for each sidecar table
- [x] 13.9 Bulk load tests pass via conformance suite: BeginBulkLoad → AddBatch → FlushBulk, idempotency, empty bulk
- [x] 13.10 Backend resolver tests pass via conformance suite: each ResolveSame* rule against known edge data
- [x] 13.11 Write wiring tests: backend flag parsing, DSN validation, pool config
- [x] 13.12 Write integration test: full daemon lifecycle with PostgreSQL backend (start, index, query, stop)
- [x] 13.13 Full go build succeeds, store_sqlite tests regression-free

## 14. Documentation

- [x] 14.1 Update `docs/cli.md` with `--backend postgres`, `--pg-dsn`, `--pg-pool-size` flags
- [x] 14.2 Update `internal/serverstack/backend.go` package doc with postgres backend option
- [x] 14.3 Write PostgreSQL setup guide (extensions required, connection string format, pool tuning)
- [x] 14.4 Update CLAUDE.md with store_pg package reference and new interface capabilities
- [x] 14.5 Update `internal/graph/store.go` interface doc to note PostgreSQL implementation exists
