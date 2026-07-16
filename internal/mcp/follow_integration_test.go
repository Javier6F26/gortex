package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/graph/store_pg"
	"github.com/zzet/gortex/internal/parser/languages"
	"github.com/zzet/gortex/internal/query"
	"github.com/zzet/gortex/internal/search"
)

// calcGo is the byte-exact source the writer indexes and stores as a blob.
const calcGo = "package a\n\nfunc Add(x, y int) int {\n\treturn x + y\n}\n"

// seedFollowerSchema is the common case: a read-only follower store over a
// freshly seeded schema.
func seedFollowerSchema(t *testing.T) *store_pg.Store {
	t.Helper()
	store, _, _ := seedFollower(t)
	return store
}

// seedFollower creates a fresh PG schema, seeds it as a writer would (code
// file + blob, markdown doc sections), and returns a read-only follower store
// plus the dsn/schema so a test can reopen a writer against the same schema.
// Skips when no test PG is configured.
func seedFollower(t *testing.T) (*store_pg.Store, string, string) {
	t.Helper()
	dsn := os.Getenv("GORTEX_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("no postgres connection; set GORTEX_TEST_PG_DSN to enable")
	}
	ctx := context.Background()
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect to test PG: %v", err)
	}
	var schema string
	if err := root.Ping(ctx); err != nil {
		root.Close()
		t.Skipf("test PG not reachable: %v", err)
	}
	schema = fmt.Sprintf("follow_it_%d", os.Getpid())
	_, _ = root.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	if _, err := root.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		root.Close()
		t.Fatalf("create schema: %v", err)
	}
	// Extensions are database-global objects placed in one schema; drop and
	// recreate them in the test schema so its search_path sees the pg_trgm /
	// vector types (mirrors the store_pg test harness).
	_, _ = root.Exec(ctx, `DROP EXTENSION IF EXISTS pg_trgm CASCADE`)
	_, _ = root.Exec(ctx, `DROP EXTENSION IF EXISTS vector CASCADE`)
	if _, err := root.Exec(ctx, `CREATE EXTENSION pg_trgm WITH SCHEMA `+schema); err != nil {
		root.Close()
		t.Fatalf("create pg_trgm: %v", err)
	}
	if _, err := root.Exec(ctx, `CREATE EXTENSION vector WITH SCHEMA `+schema); err != nil {
		root.Close()
		t.Fatalf("create vector: %v", err)
	}
	t.Cleanup(func() {
		_, _ = root.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		root.Close()
	})

	// Writer pass.
	writer, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schema})
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	const hash = "hash_calc_go"
	// Code file node + KindFile node (search_ast walks KindFile) + blob.
	writer.AddNode(&graph.Node{
		ID: "repoA/calc.go", Kind: graph.KindFile, Name: "calc.go",
		FilePath: "repoA/calc.go", RepoPrefix: "repoA", Language: "go",
	})
	writer.AddNode(&graph.Node{
		ID: "repoA/calc.go::Add", Kind: graph.KindFunction, Name: "Add",
		FilePath: "repoA/calc.go", RepoPrefix: "repoA", Language: "go",
		StartLine: 3, EndLine: 5,
	})
	// Markdown doc: two prose sections with stored section_text.
	writer.AddNode(&graph.Node{
		ID: "repoA/notes.md#intro", Kind: graph.KindDoc, Name: "Intro",
		FilePath: "repoA/notes.md", RepoPrefix: "repoA", StartLine: 1, EndLine: 3,
		Meta: map[string]any{"section_text": "# Intro\n\nFirst section."},
	})
	writer.AddNode(&graph.Node{
		ID: "repoA/notes.md#details", Kind: graph.KindDoc, Name: "Details",
		FilePath: "repoA/notes.md", RepoPrefix: "repoA", StartLine: 10, EndLine: 12,
		Meta: map[string]any{"section_text": "## Details\n\nSecond section."},
	})
	if err := writer.SetFileMetas("repoA", []graph.FileMetaRow{
		{FilePath: "repoA/calc.go", ContentHash: hash, Size: len(calcGo), NodeCount: 2},
	}); err != nil {
		t.Fatalf("SetFileMetas: %v", err)
	}
	if err := writer.PutFileBlobs([]graph.FileBlob{
		{ContentHash: hash, Body: []byte(calcGo), Size: len(calcGo)},
	}); err != nil {
		t.Fatalf("PutFileBlobs: %v", err)
	}
	_ = writer.Close()

	// Follower pass: read-only store against the same schema.
	follower, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schema, ReadOnly: true})
	if err != nil {
		t.Fatalf("open follower: %v", err)
	}
	t.Cleanup(func() { _ = follower.Close() })
	return follower, dsn, schema
}

func newFollowerServer(t *testing.T, store *store_pg.Store) *Server {
	t.Helper()
	eng := query.NewEngine(store)
	srv := NewServer(eng, store, nil, nil, zap.NewNop(), nil, MultiRepoOptions{Follow: true})
	require.True(t, srv.FollowMode())
	return srv
}

func mustJSON(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	require.NotNil(t, res)
	require.False(t, res.IsError, "tool returned error: %+v", res.Content)
	require.NotEmpty(t, res.Content)
	tc, ok := res.Content[0].(mcp.TextContent)
	require.True(t, ok, "expected TextContent, got %T", res.Content[0])
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(tc.Text), &m))
	return m
}

func call(t *testing.T, h func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error), args map[string]any) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	res, err := h(context.Background(), req)
	require.NoError(t, err)
	return res
}

// get_symbol_source on a follower serves the code symbol byte-exact from the
// blob, sliced to the node's line range, marked served_from: store with the
// content-hash etag (code-source-blobs D7 / task 4.4).
func TestFollower_GetSymbolSource_FromBlob(t *testing.T) {
	srv := newFollowerServer(t, seedFollowerSchema(t))
	out := mustJSON(t, call(t, srv.handleGetSymbolSource, map[string]any{"id": "repoA/calc.go::Add"}))

	require.Equal(t, "func Add(x, y int) int {\n\treturn x + y\n}", out["source"])
	require.Equal(t, "store", out["served_from"])
	require.Equal(t, "hash_calc_go", out["etag"])
}

// get_symbol_source on a doc node serves its stored section text, marked
// served_from: store (store-backed-doc-reads / task 4.4).
func TestFollower_GetSymbolSource_DocNode(t *testing.T) {
	srv := newFollowerServer(t, seedFollowerSchema(t))
	out := mustJSON(t, call(t, srv.handleGetSymbolSource, map[string]any{"id": "repoA/notes.md#intro"}))
	require.Equal(t, "# Intro\n\nFirst section.", out["source"])
	require.Equal(t, "store", out["served_from"])
}

// A code file the graph knows but with no stored blob (pre-blob writer)
// returns the typed follow_no_disk error, not a partial reconstruction
// (code-source-blobs / task 5.7).
func TestFollower_GetSymbolSource_BlobAbsentTypedError(t *testing.T) {
	follower, dsn, schema := seedFollower(t)
	// Add a code symbol whose file has no blob.
	w, err := store_pg.Open(context.Background(), store_pg.Config{DSN: dsn, Schema: schema})
	require.NoError(t, err)
	w.AddNode(&graph.Node{
		ID: "repoA/noblob.go::Ghost", Kind: graph.KindFunction, Name: "Ghost",
		FilePath: "repoA/noblob.go", RepoPrefix: "repoA", Language: "go",
		StartLine: 1, EndLine: 2,
	})
	_ = w.Close()

	srv := newFollowerServer(t, follower)
	res := call(t, srv.handleGetSymbolSource, map[string]any{"id": "repoA/noblob.go::Ghost"})
	require.True(t, res.IsError, "a code symbol with no blob must error, not partially reconstruct")
	tc := res.Content[0].(mcp.TextContent)
	require.Contains(t, tc.Text, "follow_no_disk")
}

// get_cfg on a follower builds the control-flow graph from the byte-exact
// file blob in the store — no working tree — instead of failing to anchor
// the node's path ("no indexed repo could anchor it") (4.2).
func TestFollower_GetCFG_FromBlob(t *testing.T) {
	srv := newFollowerServer(t, seedFollowerSchema(t))
	out := mustJSON(t, call(t, srv.handleGetCFG, map[string]any{"id": "repoA/calc.go::Add"}))
	require.Equal(t, "repoA/calc.go", out["file_path"])
	require.Equal(t, "Add", out["name"])
	require.Greater(t, out["total_blocks"].(float64), 0.0, "CFG must have at least an entry block")
}

// get_cfg for a resolvable function whose file has no stored blob returns
// the typed follow_no_disk marker — never a path-anchoring error (4.2).
func TestFollower_GetCFG_NoBlobTypedError(t *testing.T) {
	follower, dsn, schema := seedFollower(t)
	w, err := store_pg.Open(context.Background(), store_pg.Config{DSN: dsn, Schema: schema})
	require.NoError(t, err)
	w.AddNode(&graph.Node{
		ID: "repoA/noblob.go::Ghost", Kind: graph.KindFunction, Name: "Ghost",
		FilePath: "repoA/noblob.go", RepoPrefix: "repoA", Language: "go",
		StartLine: 1, EndLine: 3,
	})
	_ = w.Close()

	srv := newFollowerServer(t, follower)
	res := call(t, srv.handleGetCFG, map[string]any{"id": "repoA/noblob.go::Ghost"})
	require.True(t, res.IsError)
	tc := res.Content[0].(mcp.TextContent)
	require.Contains(t, tc.Text, "follow_no_disk")
	require.NotContains(t, tc.Text, "anchor", "must not leak the path-anchoring error for a resolvable node")
}

// read_file on a follower reconstructs a markdown doc from stored section text
// in line order, marked served_from: store (store-backed-doc-reads / task 4.3).
func TestFollower_ReadFile_MarkdownFromStore(t *testing.T) {
	srv := newFollowerServer(t, seedFollowerSchema(t))
	out := mustJSON(t, call(t, srv.handleReadFile, map[string]any{"path": "repoA/notes.md"}))

	content, _ := out["content"].(string)
	require.Contains(t, content, "First section.")
	require.Contains(t, content, "Second section.")
	require.Less(t, strings.Index(content, "First section."), strings.Index(content, "Second section."),
		"sections must appear in start-line order")
	require.Equal(t, "store", out["served_from"])
}

// read_file on a follower serves a code file byte-exact from its blob.
func TestFollower_ReadFile_CodeFromBlob(t *testing.T) {
	srv := newFollowerServer(t, seedFollowerSchema(t))
	out := mustJSON(t, call(t, srv.handleReadFile, map[string]any{"path": "repoA/calc.go"}))
	require.Equal(t, calcGo, out["content"])
	require.Equal(t, "store", out["served_from"])
}

// search_text on a follower runs over the blob-backed trigram searcher, no
// disk, marked served_from: store (code-source-blobs / task 5.6).
func TestFollower_SearchText_Diskless(t *testing.T) {
	srv := newFollowerServer(t, seedFollowerSchema(t))
	// Literal.
	out := mustJSON(t, call(t, srv.handleSearchText, map[string]any{"query": "func Add"}))
	require.Equal(t, "store", out["served_from"])
	require.EqualValues(t, 1, out["count"])
	// Regex.
	out = mustJSON(t, call(t, srv.handleSearchText, map[string]any{"query": `func \w+\(`, "regexp": true}))
	require.EqualValues(t, 1, out["count"])
}

// search_ast on a follower parses from blob bytes, no disk (task 5.6).
func TestFollower_SearchAST_Diskless(t *testing.T) {
	srv := newFollowerServer(t, seedFollowerSchema(t))
	out := mustJSON(t, call(t, srv.handleSearchAST, map[string]any{
		"pattern":  "(function_declaration name: (identifier) @match)",
		"language": "go",
	}))
	require.EqualValues(t, 1, out["total"], "should match the Add function from the blob")
}

// The follower's store refuses writes — the read-only backstop of the write
// seal (task 6.1: no write reaches the shared schema).
func TestFollower_StoreRefusesWrites(t *testing.T) {
	store := seedFollowerSchema(t)
	err := store.PutFileBlobs([]graph.FileBlob{{ContentHash: "x", Body: []byte("y"), Size: 1}})
	require.ErrorIs(t, err, store_pg.ErrReadOnlyStore)
}

// A running follower observes rows the writer commits after boot, with no
// restart — reads are live SQL against the shared schema (task 6.2).
func TestFollower_ObservesWriterCommitsLive(t *testing.T) {
	follower, dsn, schema := seedFollower(t)
	srv := newFollowerServer(t, follower)

	// Not present yet.
	require.Nil(t, srv.engineFor(context.Background()).GetSymbol("repoA/calc.go::Sub"))

	// Writer commits a new symbol after the follower is serving.
	w, err := store_pg.Open(context.Background(), store_pg.Config{DSN: dsn, Schema: schema})
	require.NoError(t, err)
	w.AddNode(&graph.Node{
		ID: "repoA/calc.go::Sub", Kind: graph.KindFunction, Name: "Sub",
		FilePath: "repoA/calc.go", RepoPrefix: "repoA", Language: "go",
		StartLine: 7, EndLine: 9,
	})
	_ = w.Close()

	// Follower sees it with no reload machinery.
	got := srv.engineFor(context.Background()).GetSymbol("repoA/calc.go::Sub")
	require.NotNil(t, got, "follower must observe the writer's commit live")
	require.Equal(t, "Sub", got.Name)
}

// smart_context runs on a diskless follower without panicking, and any source
// excerpts it embeds for code symbols are served byte-exact from blobs
// (code-source-blobs: smart_context excerpts on a diskless follower).
func TestFollower_SmartContext_Diskless(t *testing.T) {
	srv := newFollowerServer(t, seedFollowerSchema(t))
	// The guarantee is no panic / no error on a diskless follower (task 6.1);
	// mustJSON asserts the result is not an error. Any embedded source excerpt
	// must be marked store-served.
	out := mustJSON(t, call(t, srv.handleSmartContext, map[string]any{"task": "Add two integers"}))
	require.Contains(t, out, "relevant_symbols")
	if syms, ok := out["relevant_symbols"].([]any); ok {
		for _, s := range syms {
			m, _ := s.(map[string]any)
			if _, hasSource := m["source"]; hasSource {
				require.Equal(t, "store", m["served_from"], "embedded excerpts must be marked store-served")
			}
		}
	}
}

// detect_changes on a diskless follower has no working tree to diff, so it
// must error with follow_no_disk (like review / review_pack) instead of
// reporting a false "risk NONE" empty changeset.
func TestFollower_DetectChanges_TypedError(t *testing.T) {
	srv := newFollowerServer(t, seedFollowerSchema(t))
	res := call(t, srv.handleDetectChanges, map[string]any{})
	require.True(t, res.IsError, "detect_changes on a follower must error, not report no changes")
	tc := res.Content[0].(mcp.TextContent)
	require.Contains(t, tc.Text, "follow_no_disk")
	require.Contains(t, tc.Text, "detect_changes")
}

// Index provenance the writer stamps into repo_index_state is served
// unchanged through a follower's list_repos — the follower reads it, never
// computes it (it has no working tree).
func TestFollower_ProvenanceVisibleThroughFollower(t *testing.T) {
	follower, dsn, schema := seedFollower(t)

	// Writer stamps per-repo provenance after boot.
	w, err := store_pg.Open(context.Background(), store_pg.Config{DSN: dsn, Schema: schema})
	require.NoError(t, err)
	require.NoError(t, w.SetRepoIndexState(graph.RepoIndexState{
		RepoPrefix: "repoA",
		IndexedSHA: "deadbeefcafe",
		IndexedAt:  1_700_000_000,
	}))
	_ = w.Close()

	srv := newFollowerServer(t, follower)
	payload := srv.buildListReposPayload(context.Background())
	repos, ok := payload["repos"].([]map[string]any)
	require.True(t, ok, "repos must be entry objects, got %T", payload["repos"])

	var got map[string]any
	for _, r := range repos {
		if r["name"] == "repoA" {
			got = r
			break
		}
	}
	require.NotNil(t, got, "repoA must be listed; got %+v", repos)
	require.Equal(t, "deadbeefcafe", got["last_synced_sha"])
	require.Equal(t, "2023-11-14T22:13:20Z", got["last_synced_at"])
}

// A body-only underscored phrase must return its Markdown section through a
// postgres follower's live search_symbols corpus=docs path — the end-to-end
// acceptance check for docs-corpus-search body matching. The follower's
// engine is wired exactly as production (initialSearchBackend ->
// SymbolSearcherBackend); the store-routed backend's Count() is 0 on a
// follower that never indexes, so this exercises the readiness-gate fix that
// keeps the body-aware store path live (search.IsStoreRouted).
func TestFollower_SearchSymbols_DocBodyMatch(t *testing.T) {
	follower, dsn, schema := seedFollower(t)

	// Index a real Markdown doc through the production extractor so the whole
	// chain — prose extraction (underscore preservation) → stored section_text
	// → follower search — is exercised, not a hand-written section_text. The
	// heading carries NONE of the query terms; the underscored phrase lives
	// only in the section body.
	const apiMD = "# API Reference\n\n" +
		"Call `branch_track(repository_url, branch)` to start syncing the vault.\n"
	ext := languages.NewMarkdownExtractor()
	res, err := ext.Extract("repoA/api.md", []byte(apiMD))
	require.NoError(t, err)
	var docID string
	w, err := store_pg.Open(context.Background(), store_pg.Config{DSN: dsn, Schema: schema})
	require.NoError(t, err)
	for _, n := range res.Nodes {
		if n.Kind != graph.KindDoc {
			continue
		}
		n.RepoPrefix = "repoA"
		body, _ := n.Meta["section_text"].(string)
		require.Contains(t, body, "branch_track", "extractor must preserve identifier underscores")
		w.AddNode(n)
		docID = n.ID
	}
	require.NotEmpty(t, docID, "extractor must emit a KindDoc section")
	require.NoError(t, w.BuildSymbolIndex()) // doc-body FTS index (idempotent)
	_ = w.Close()

	// Follower server with the store-routed search backend wired exactly as
	// production does. Without the readiness-gate fix the backend's zero
	// Count() would send this query to the name-only substring fallback and
	// the body match would be lost.
	eng := query.NewEngine(follower)
	eng.SetSearch(search.NewSymbolSearcherBackend(follower))
	srv := NewServer(eng, follower, nil, nil, zap.NewNop(), nil, MultiRepoOptions{Follow: true})
	require.True(t, srv.FollowMode())

	// Body-only underscored phrase → the section is returned.
	out := mustJSON(t, call(t, srv.handleSearchSymbols, map[string]any{
		"query":  "branch_track repository_url",
		"corpus": "docs",
		"repo":   "*",
		"limit":  10,
	}))
	require.Contains(t, resultIDs(out), docID,
		"body-only underscored phrase must return its section through the follower")

	// The heading/name channel still works: a query on the section heading.
	out = mustJSON(t, call(t, srv.handleSearchSymbols, map[string]any{
		"query":  "API Reference",
		"corpus": "docs",
		"repo":   "*",
		"limit":  10,
	}))
	require.Contains(t, resultIDs(out), docID,
		"heading/name matches must keep working")
}

// contracts on a diskless follower answers from the store's persisted
// kind=contract nodes — the writer never re-runs indexing on the follower,
// so without the store-backed effectiveContractRegistry fallback this
// would falsely report "no contract registry available — index a
// repository first" (fix-follower-contract-registry 1.3).
func TestFollower_Contracts_FromStore(t *testing.T) {
	follower, dsn, schema := seedFollower(t)

	// Writer persists contract nodes the way the indexer / wrapper-inline
	// path does (see commitInlinedContractToGraph): full record on Meta.
	w, err := store_pg.Open(context.Background(), store_pg.Config{DSN: dsn, Schema: schema})
	require.NoError(t, err)
	w.AddNode(&graph.Node{
		ID: "http::GET::/v1/users", Kind: graph.KindContract, Name: "http::GET::/v1/users",
		FilePath: "repoA/api.go", RepoPrefix: "repoA", Language: "contract",
		Meta: map[string]any{
			"type": "http", "role": "provider", "symbol_id": "repoA/api.go::ListUsers",
			"line": 10, "confidence": 0.9,
			"contract_meta": map[string]any{"path": "/v1/users", "method": "GET"},
		},
	})
	w.AddNode(&graph.Node{
		ID: "grpc::UserService/Get", Kind: graph.KindContract, Name: "grpc::UserService/Get",
		FilePath: "repoA/client.go", RepoPrefix: "repoA", Language: "contract",
		Meta: map[string]any{
			"type": "grpc", "role": "consumer", "symbol_id": "repoA/client.go::getUser",
			"line": 20, "confidence": 0.8,
		},
	})
	_ = w.Close()

	srv := newFollowerServer(t, follower)
	out := mustJSON(t, call(t, srv.handleContracts, map[string]any{"action": "list", "all_repos": true}))
	require.EqualValues(t, 2, out["total"], "both persisted contracts must be listed through the follower")
}

// contracts on a follower whose store genuinely holds no contract nodes
// still errors honestly — the store-backed fallback must not manufacture
// an empty registry that hides the real state (fix-follower-contract-registry 1.3).
func TestFollower_Contracts_EmptyStoreErrors(t *testing.T) {
	// seedFollowerSchema seeds code + doc nodes but no contracts.
	srv := newFollowerServer(t, seedFollowerSchema(t))
	res := call(t, srv.handleContracts, map[string]any{"action": "list", "all_repos": true})
	require.True(t, res.IsError, "an empty store must still error, not report zero contracts as success")
	tc := res.Content[0].(mcp.TextContent)
	require.Contains(t, tc.Text, "index a repository first")
}

// A store-served doc read's etag is stable across identical reconstructions
// (task 4.6: etag derives from content, not wall clock / disk).
func TestFollower_DocEtagStable(t *testing.T) {
	srv := newFollowerServer(t, seedFollowerSchema(t))
	a := mustJSON(t, call(t, srv.handleReadFile, map[string]any{"path": "repoA/notes.md"}))
	b := mustJSON(t, call(t, srv.handleReadFile, map[string]any{"path": "repoA/notes.md"}))
	require.NotEmpty(t, a["etag"])
	require.Equal(t, a["etag"], b["etag"], "identical reconstructions must share an etag")
}
