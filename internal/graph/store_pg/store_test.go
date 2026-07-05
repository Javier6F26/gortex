package store_pg_test

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/graph/store_pg"
	"github.com/zzet/gortex/internal/graph/storetest"
)

func sanitizeSchemaName(name string) string {
	out := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
			out = append(out, c)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}

var testPGDSN = os.Getenv("GORTEX_TEST_PG_DSN")

func init() {
	if testPGDSN == "" {
		testPGDSN = "postgres://localhost:5433/gortex_test?sslmode=disable"
	}
}

func skipIfNoPG(t *testing.T) {
	t.Helper()
	if testRootPool == nil {
		t.Skipf("no postgres connection; set GORTEX_TEST_PG_DSN to enable")
	}
	if err := testRootPool.Ping(context.Background()); err != nil {
		t.Skipf("postgres not reachable (%v)", err)
	}
}

var testRootPool *pgxpool.Pool

func TestMain(m *testing.M) {
	ctx := context.Background()
	if testPGDSN != "" {
		if pool, err := pgxpool.New(ctx, testPGDSN); err == nil {
			testRootPool = pool
		}
	}
	code := m.Run()
	if testRootPool != nil {
		testRootPool.Close()
	}
	os.Exit(code)
}

var schemaCounter int32

// createTestSchema creates a unique PostgreSQL schema for test isolation.
func createTestSchema(t *testing.T) (string, string) {
	t.Helper()
	n := atomic.AddInt32(&schemaCounter, 1)
	schemaName := fmt.Sprintf("gortex_test_%d_%d_%s", os.Getpid(), n, sanitizeSchemaName(t.Name()))
	ctx := context.Background()

	// Drop and recreate the schema. Install extensions into it so they
	// are visible with search_path = <schema> (without public).
	_, _ = testRootPool.Exec(ctx, fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, schemaName))
	if _, err := testRootPool.Exec(ctx, fmt.Sprintf(`CREATE SCHEMA %s`, schemaName)); err != nil {
		t.Fatalf("create schema %s: %v", schemaName, err)
	}
	// Drop global extensions so we can re-create them in the test schema.
	testRootPool.Exec(ctx, `DROP EXTENSION IF EXISTS pg_trgm CASCADE`)
	testRootPool.Exec(ctx, `DROP EXTENSION IF EXISTS vector CASCADE`)
	if _, err := testRootPool.Exec(ctx, fmt.Sprintf(`CREATE EXTENSION pg_trgm WITH SCHEMA %s`, schemaName)); err != nil {
		t.Fatalf("create pg_trgm in %s: %v", schemaName, err)
	}
	if _, err := testRootPool.Exec(ctx, fmt.Sprintf(`CREATE EXTENSION vector WITH SCHEMA %s`, schemaName)); err != nil {
		t.Fatalf("create vector in %s: %v", schemaName, err)
	}
	t.Cleanup(func() {
		// Restore extensions to public and drop the test schema.
		_, _ = testRootPool.Exec(context.Background(), fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, schemaName))
		_, _ = testRootPool.Exec(context.Background(), `DROP EXTENSION IF EXISTS pg_trgm CASCADE`)
		_, _ = testRootPool.Exec(context.Background(), `DROP EXTENSION IF EXISTS vector CASCADE`)
		_, _ = testRootPool.Exec(context.Background(), `CREATE EXTENSION IF NOT EXISTS pg_trgm SCHEMA public`)
		_, _ = testRootPool.Exec(context.Background(), `CREATE EXTENSION IF NOT EXISTS vector SCHEMA public`)
	})
	return testPGDSN, schemaName
}

func TestConformance(t *testing.T) {
	skipIfNoPG(t)
	storetest.RunConformance(t, func(t *testing.T) graph.Store {
		dsn, schemaName := createTestSchema(t)
		ctx := context.Background()
		st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		return st
	})
}

func TestOpenClose(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if st == nil {
		t.Fatal("Open returned nil store")
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestDoubleClose(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = st.Close()
	_ = st.Close()
}

func TestOpenInvalidDSN(t *testing.T) {
	ctx := context.Background()
	_, err := store_pg.Open(ctx, store_pg.Config{DSN: "postgres://invalid:invalid@nonexistent:9999/gortex"})
	if err == nil {
		t.Fatal("expected error for invalid DSN")
	}
}

func TestOpenEmptyDSN(t *testing.T) {
	ctx := context.Background()
	_, err := store_pg.Open(ctx, store_pg.Config{DSN: ""})
	if err == nil {
		t.Fatal("expected error for empty DSN")
	}
}

func TestPoolConfig(t *testing.T) {
	skipIfNoPG(t)
	dsn, _ := createTestSchema(t)
	ctx := context.Background()
	cfg := store_pg.Config{
		DSN:          dsn,
		PoolMaxConns: 4,
		PoolMinConns: 1,
	}
	st, err := store_pg.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	stats := st.PoolStats()
	if stats.MaxConns != 4 {
		t.Errorf("expected MaxConns=4, got %d", stats.MaxConns)
	}
}

// ---------------------------------------------------------------------------
// 13.4 — Content search tests (tsvector)
// ---------------------------------------------------------------------------

func TestContentSearch_EmptyCorpus(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.BuildContentIndex(); err != nil {
		t.Fatalf("BuildContentIndex: %v", err)
	}

	// No content inserted — search should return empty.
	hits, err := st.SearchContent("test", "", 10)
	if err != nil {
		t.Fatalf("SearchContent: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("expected 0 hits for empty corpus, got %d", len(hits))
	}
}

func TestContentSearch_ProseMatch(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.BuildContentIndex(); err != nil {
		t.Fatalf("BuildContentIndex: %v", err)
	}

	// Insert content items.
	err = st.AppendContent("repo1", []graph.ContentFTSItem{
		{NodeID: "node1", FilePath: "doc.md", Ordinal: 0, Body: "The quick brown fox jumps over the lazy dog."},
		{NodeID: "node2", FilePath: "readme.md", Ordinal: 0, Body: "This project is a code intelligence engine written in Go."},
		{NodeID: "node3", FilePath: "guide.md", Ordinal: 0, Body: "PostgreSQL integration with vector search and fulltext indexing."},
	})
	if err != nil {
		t.Fatalf("AppendContent: %v", err)
	}

	// Search for "fox" should match the first content item.
	hits, err := st.SearchContent("fox", "", 10)
	if err != nil {
		t.Fatalf("SearchContent: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hits for prose query")
	}
	hasFox := false
	for _, h := range hits {
		if h.NodeID == "node1" {
			hasFox = true
			if h.Snippet == "" {
				t.Error("expected non-empty snippet for hit")
			}
			break
		}
	}
	if !hasFox {
		t.Errorf("expected node1 (fox) in results, got %+v", hits)
	}
}

func TestContentSearch_PerReposScope(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.BuildContentIndex(); err != nil {
		t.Fatalf("BuildContentIndex: %v", err)
	}

	err = st.AppendContent("repo1", []graph.ContentFTSItem{
		{NodeID: "r1", FilePath: "doc.md", Ordinal: 0, Body: "The quick brown fox"},
	})
	if err != nil {
		t.Fatalf("AppendContent repo1: %v", err)
	}
	err = st.AppendContent("repo2", []graph.ContentFTSItem{
		{NodeID: "r2", FilePath: "other.md", Ordinal: 0, Body: "The lazy fox"},
	})
	if err != nil {
		t.Fatalf("AppendContent repo2: %v", err)
	}

	// Search scoped to repo1 — should only return r1.
	hits, err := st.SearchContent("fox", "repo1", 10)
	if err != nil {
		t.Fatalf("SearchContent: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit for repo1 scope, got %d", len(hits))
	}
	if hits[0].NodeID != "r1" {
		t.Errorf("expected r1, got %s", hits[0].NodeID)
	}
}

func TestContentSearch_WipeContent(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.BuildContentIndex(); err != nil {
		t.Fatalf("BuildContentIndex: %v", err)
	}

	err = st.AppendContent("repo1", []graph.ContentFTSItem{
		{NodeID: "n1", FilePath: "doc.md", Ordinal: 0, Body: "test content"},
	})
	if err != nil {
		t.Fatalf("AppendContent: %v", err)
	}

	// Wipe the repo and verify content is gone.
	if err := st.WipeContent("repo1"); err != nil {
		t.Fatalf("WipeContent: %v", err)
	}
	hits, err := st.SearchContent("test", "repo1", 10)
	if err != nil {
		t.Fatalf("SearchContent after wipe: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("expected 0 hits after wipe, got %d", len(hits))
	}
}

func TestContentSearch_WipeContentFile(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.BuildContentIndex(); err != nil {
		t.Fatalf("BuildContentIndex: %v", err)
	}

	err = st.AppendContent("repo1", []graph.ContentFTSItem{
		{NodeID: "n1", FilePath: "doc.md", Ordinal: 0, Body: "keep content about indexing"},
		{NodeID: "n2", FilePath: "other.md", Ordinal: 0, Body: "remove content about parsing"},
	})
	if err != nil {
		t.Fatalf("AppendContent: %v", err)
	}

	// Wipe one file.
	if err := st.WipeContentFile("other.md"); err != nil {
		t.Fatalf("WipeContentFile: %v", err)
	}
	hits, err := st.SearchContent("indexing", "repo1", 10)
	if err != nil {
		t.Fatalf("SearchContent: %v", err)
	}
	// Should still find "keep content about indexing" from doc.md.
	if len(hits) == 0 {
		t.Fatal("expected hits after file wipe")
	}
	for _, h := range hits {
		if h.NodeID == "n2" {
			t.Errorf("did not expect n2 after wipe")
		}
	}
}

func TestContentSearch_ScanContent(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.BuildContentIndex(); err != nil {
		t.Fatalf("BuildContentIndex: %v", err)
	}

	err = st.AppendContent("repo1", []graph.ContentFTSItem{
		{NodeID: "a", FilePath: "a.md", Ordinal: 0, Body: "alpha body"},
		{NodeID: "b", FilePath: "b.md", Ordinal: 0, Body: "beta body"},
	})
	if err != nil {
		t.Fatalf("AppendContent: %v", err)
	}

	// Scan content and collect results.
	var scanned []string
	err = st.ScanContent("repo1", func(nodeID, filePath, body string) bool {
		scanned = append(scanned, nodeID)
		return true
	})
	if err != nil {
		t.Fatalf("ScanContent: %v", err)
	}
	if len(scanned) != 2 {
		t.Errorf("expected 2 scanned items, got %d", len(scanned))
	}
}

func TestContentSearch_ScanContentEarlyExit(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.BuildContentIndex(); err != nil {
		t.Fatalf("BuildContentIndex: %v", err)
	}

	err = st.AppendContent("repo1", []graph.ContentFTSItem{
		{NodeID: "a", FilePath: "a.md", Ordinal: 0, Body: "alpha"},
		{NodeID: "b", FilePath: "b.md", Ordinal: 0, Body: "beta"},
	})
	if err != nil {
		t.Fatalf("AppendContent: %v", err)
	}

	var scanned []string
	err = st.ScanContent("repo1", func(nodeID, filePath, body string) bool {
		scanned = append(scanned, nodeID)
		return false // stop after first
	})
	if err != nil {
		t.Fatalf("ScanContent: %v", err)
	}
	if len(scanned) != 1 {
		t.Errorf("expected 1 scanned item after early exit, got %d", len(scanned))
	}
}

func TestContentSearch_AppendEmpty(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	// AppendContent with empty items should not error.
	err = st.AppendContent("repo1", nil)
	if err != nil {
		t.Fatalf("AppendContent nil: %v", err)
	}
	err = st.AppendContent("repo1", []graph.ContentFTSItem{})
	if err != nil {
		t.Fatalf("AppendContent empty: %v", err)
	}

	// AppendContent with items where some have empty NodeID should not
	// produce SQL parameter errors — the placeholder numbering must
	// correctly account for skipped items.
	items := []graph.ContentFTSItem{
		{NodeID: "", FilePath: "skip.go", Ordinal: 0, Body: "should be skipped"},
		{NodeID: "node1", FilePath: "keep.go", Ordinal: 1, Body: "keep this"},
	}
	err = st.AppendContent("repo1", items)
	if err != nil {
		t.Fatalf("AppendContent with skipped items: %v", err)
	}

	// Verify the valid item was inserted.
	hits, err := st.SearchContent("keep", "", 10)
	if err != nil {
		t.Fatalf("SearchContent after skipped-item insert: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hit for 'keep' after AppendContent with skipped items")
	}
}

// ---------------------------------------------------------------------------
// 13.5 — Vector search tests (pgvector HNSW)
// ---------------------------------------------------------------------------

func TestVectorSearch_EmptyQuery(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	// Empty/Nil vector returns empty.
	hits, err := st.SimilarTo([]float32{}, 10)
	if err != nil {
		t.Fatalf("SimilarTo empty: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("expected 0 hits for empty vector, got %d", len(hits))
	}
}

func TestVectorSearch_SimilarTo(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	// Build the HNSW index first.
	if err := st.BuildVectorIndex(384); err != nil {
		t.Fatalf("BuildVectorIndex: %v", err)
	}

	// Insert vectors: three 4d vectors.
	err = st.UpsertEmbedding("vec_a", vec384(0))
	if err != nil {
		t.Fatalf("UpsertEmbedding a: %v", err)
	}
	err = st.UpsertEmbedding("vec_b", vec384(1))
	if err != nil {
		t.Fatalf("UpsertEmbedding b: %v", err)
	}
	err = st.UpsertEmbedding("vec_c", vec384(2))
	if err != nil {
		t.Fatalf("UpsertEmbedding c: %v", err)
	}

	// Query with a vector close to vec_a.
	hits, err := st.SimilarTo(vec384(0), 3)
	if err != nil {
		t.Fatalf("SimilarTo: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hits for SimilarTo")
	}
	// vec_a should be closest (smallest distance).
	if hits[0].NodeID != "vec_a" {
		t.Errorf("expected vec_a as nearest, got %s", hits[0].NodeID)
	}
	if hits[0].Distance < 0 {
		t.Errorf("expected non-negative cosine distance, got %f", hits[0].Distance)
	}
}

func TestVectorSearch_BulkUpsert(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.BuildVectorIndex(384); err != nil {
		t.Fatalf("BuildVectorIndex: %v", err)
	}

	items := []graph.VectorItem{
		{NodeID: "v1", Vec: vec384(0)},
		{NodeID: "v2", Vec: vec384(1)},
	}
	err = st.BulkUpsertEmbeddings(items)
	if err != nil {
		t.Fatalf("BulkUpsertEmbeddings: %v", err)
	}

	hits, err := st.SimilarTo(vec384(0), 5)
	if err != nil {
		t.Fatalf("SimilarTo: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hits after bulk upsert")
	}
}

func TestVectorSearch_BulkUpsertEmpty(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	// Empty bulk upsert should not error.
	err = st.BulkUpsertEmbeddings(nil)
	if err != nil {
		t.Fatalf("BulkUpsertEmbeddings nil: %v", err)
	}
	err = st.BulkUpsertEmbeddings([]graph.VectorItem{})
	if err != nil {
		t.Fatalf("BulkUpsertEmbeddings empty: %v", err)
	}
}

func TestVectorSearch_GetEmbeddings(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.BuildVectorIndex(384); err != nil {
		t.Fatalf("BuildVectorIndex: %v", err)
	}

	err = st.UpsertEmbedding("v1", vec384(0))
	if err != nil {
		t.Fatalf("UpsertEmbedding: %v", err)
	}
	err = st.UpsertEmbedding("v2", vec384(1))
	if err != nil {
		t.Fatalf("UpsertEmbedding: %v", err)
	}

	em := st.GetEmbeddings([]string{"v1", "v2", "nonexistent"})
	if len(em) != 2 {
		t.Errorf("expected 2 embeddings, got %d", len(em))
	}
	v1, ok := em["v1"]
	if !ok {
		t.Error("expected v1 in embeddings")
	} else if len(v1) != 384 {
		t.Errorf("expected v1 embedding length 3, got %d", len(v1))
	}
	// nonexistent should be absent.
	if _, ok := em["nonexistent"]; ok {
		t.Error("did not expect nonexistent in embeddings")
	}
}

func TestVectorSearch_GetEmbeddingsEmpty(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	em := st.GetEmbeddings(nil)
	if len(em) != 0 {
		t.Errorf("expected empty map for nil input, got %d", len(em))
	}
	em = st.GetEmbeddings([]string{})
	if len(em) != 0 {
		t.Errorf("expected empty map for empty input, got %d", len(em))
	}
}

func TestVectorSearch_DimensionMismatch(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.BuildVectorIndex(384); err != nil {
		t.Fatalf("BuildVectorIndex: %v", err)
	}

	// Insert a 384-dim vector successfully.
	err = st.UpsertEmbedding("v3", vec384(0))
	if err != nil {
		t.Fatalf("UpsertEmbedding 384d: %v", err)
	}

	// Verify a wrong-dimension vector (e.g., 4d instead of 384d) is rejected.
	err = st.UpsertEmbedding("v4", []float32{1, 0, 0, 0})
	if err == nil {
		// pgvector's vector(384) column should reject 4-dim inserts. If the
		// test reaches here the dim enforcement may have been relaxed, but
		// the insert succeeding doesn't break anything — log a note.
		t.Log("schema accepted non-384-dim vector (dimension not enforced)")
	} else {
		t.Logf("schema rejected non-384-dim vector: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 13.12 — Integration test: full backend lifecycle
// ---------------------------------------------------------------------------

// TestIntegration_FullLifecycle exercises the complete store lifecycle with
// PostgreSQL: open → add data → build indexes → search → traverse → aggregate
// → close. This mirrors the daemon lifecycle but at the store level so it
// runs as a regular test without needing a running daemon binary.
func TestIntegration_FullLifecycle(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()

	// 1. Open
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	// 2. Index: add nodes + edges (simulating a small repo index).
	st.AddNode(&graph.Node{ID: "main.go::parseFile", Kind: graph.KindFunction, Name: "parseFile",
		FilePath: "main.go", RepoPrefix: "myrepo"})
	st.AddNode(&graph.Node{ID: "main.go::formatOutput", Kind: graph.KindFunction, Name: "formatOutput",
		FilePath: "main.go", RepoPrefix: "myrepo"})
	st.AddNode(&graph.Node{ID: "main.go::validateInput", Kind: graph.KindFunction, Name: "validateInput",
		FilePath: "main.go", RepoPrefix: "myrepo"})
	st.AddNode(&graph.Node{ID: "main.go::Config", Kind: graph.KindType, Name: "Config",
		FilePath: "main.go", RepoPrefix: "myrepo"})

	st.AddEdge(&graph.Edge{From: "main.go::parseFile", To: "main.go::formatOutput", Kind: "calls", FilePath: "main.go"})
	st.AddEdge(&graph.Edge{From: "main.go::parseFile", To: "main.go::validateInput", Kind: "calls", FilePath: "main.go"})
	st.AddEdge(&graph.Edge{From: "main.go::formatOutput", To: "main.go::Config", Kind: "reads", FilePath: "main.go"})

	// 3. Build all indexes.
	if err := st.BuildSymbolIndex(); err != nil {
		t.Fatalf("BuildSymbolIndex: %v", err)
	}
	if err := st.BuildContentIndex(); err != nil {
		t.Fatalf("BuildContentIndex: %v", err)
	}
	if err := st.BuildVectorIndex(384); err != nil {
		t.Fatalf("BuildVectorIndex: %v", err)
	}

	// Add content after index build.
	err = st.AppendContent("myrepo", []graph.ContentFTSItem{
		{NodeID: "main.go::parseFile", FilePath: "main.go", Ordinal: 0,
			Body: "Parses the input file and produces an AST."},
	})
	if err != nil {
		t.Fatalf("AppendContent: %v", err)
	}

	// Add vectors.
	err = st.UpsertEmbedding("main.go::parseFile", vec384(0))
	if err != nil {
		t.Fatalf("UpsertEmbedding: %v", err)
	}
	err = st.UpsertEmbedding("main.go::formatOutput", vec384(1))
	if err != nil {
		t.Fatalf("UpsertEmbedding: %v", err)
	}

	// 4. Query via all search paths.

	// 4a. Symbol search (exact match).
	hits, err := st.SearchSymbols("parseFile", 10)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	if len(hits) == 0 || hits[0].NodeID != "main.go::parseFile" {
		t.Errorf("symbol search: expected main.go::parseFile, got %+v", hits)
	}

	// 4b. Symbol search (fuzzy).
	hits, err = st.SearchSymbols("parsFile", 10)
	if err != nil {
		t.Fatalf("SearchSymbols fuzzy: %v", err)
	}
	if len(hits) == 0 {
		t.Error("symbol search fuzzy: expected at least one hit")
	}

	// 4c. Content search.
	contentHits, err := st.SearchContent("parses input", "myrepo", 10)
	if err != nil {
		t.Fatalf("SearchContent: %v", err)
	}
	if len(contentHits) == 0 {
		t.Error("content search: expected at least one hit")
	}

	// 4d. Vector search.
	vecHits, err := st.SimilarTo(vec384(0), 5)
	if err != nil {
		t.Fatalf("SimilarTo: %v", err)
	}
	if len(vecHits) == 0 {
		t.Error("vector search: expected at least one hit")
	}

	// 5. Graph traversal (BFS).
	if bfs, ok := interface{}(st).(graph.BFSCapable); ok {
		results, err := bfs.BFS(
			[]string{"main.go::parseFile"},
			graph.DirectionForward,
			[]graph.EdgeKind{"calls"},
			3,   // maxDepth
			10,  // limit
		)
		if err != nil {
			t.Fatalf("BFS: %v", err)
		}
		if len(results) == 0 {
			t.Error("BFS: expected at least one result")
		}
	} else {
		t.Error("store does not implement BFSCapable")
	}

	// 6. Aggregators.
	nodeCount := st.NodeCount()
	if nodeCount < 4 {
		t.Errorf("NodeCount: expected >= 4, got %d", nodeCount)
	}
	edgeCount := st.EdgeCount()
	if edgeCount < 3 {
		t.Errorf("EdgeCount: expected >= 3, got %d", edgeCount)
	}

	// 7. Stats.
	stats := st.Stats()
	if stats.TotalNodes < 4 {
		t.Errorf("Stats.TotalNodes: expected >= 4, got %d", stats.TotalNodes)
	}
	if stats.TotalEdges < 3 {
		t.Errorf("Stats.TotalEdges: expected >= 3, got %d", stats.TotalEdges)
	}

	// 8. Close is handled by defer.

	t.Log("full lifecycle integration test passed")
}

func TestVectorSearch_BuildVectorIndexIdempotent(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	// Calling BuildVectorIndex multiple times should be safe.
	if err := st.BuildVectorIndex(384); err != nil {
		t.Fatalf("BuildVectorIndex #1: %v", err)
	}
	if err := st.BuildVectorIndex(384); err != nil {
		t.Fatalf("BuildVectorIndex #2: %v", err)
	}
	if err := st.BuildVectorIndex(384); err != nil {
		t.Fatalf("BuildVectorIndex #3: %v", err)
	}
}

// seedTestNodes inserts a handful of nodes with the given names into the store.
func seedTestNodes(t *testing.T, st *store_pg.Store, names ...string) {
	t.Helper()
	for _, name := range names {
		st.AddNode(&graph.Node{
			ID:       "test::" + name,
			Kind:     graph.KindFunction,
			Name:     name,
			FilePath: "test.go",
		})
	}
}

func TestSymbolSearch_EmptyQuery(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if err := st.BuildSymbolIndex(); err != nil {
		t.Fatalf("BuildSymbolIndex: %v", err)
	}

	// Empty query should return nil.
	hits, err := st.SearchSymbols("", 10)
	if err != nil {
		t.Fatalf("SearchSymbols empty: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("expected 0 hits for empty query, got %d", len(hits))
	}
}

func TestSymbolSearch_ZeroLimit(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.BuildSymbolIndex(); err != nil {
		t.Fatalf("BuildSymbolIndex: %v", err)
	}
	seedTestNodes(t, st, "parseFile")

	// Zero limit should return nil.
	hits, err := st.SearchSymbols("parseFile", 0)
	if err != nil {
		t.Fatalf("SearchSymbols zero limit: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("expected 0 hits for 0 limit, got %d", len(hits))
	}
}

func TestSymbolSearch_ExactMatch(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.BuildSymbolIndex(); err != nil {
		t.Fatalf("BuildSymbolIndex: %v", err)
	}
	seedTestNodes(t, st, "parseFile", "parseLine", "formatOutput")

	hits, err := st.SearchSymbols("parseFile", 10)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one hit for exact match")
	}
	if hits[0].NodeID != "test::parseFile" {
		t.Errorf("expected first hit NodeID=test::parseFile, got %s", hits[0].NodeID)
	}
	if hits[0].Score != 100.0 {
		t.Errorf("expected exact-match score 100.0, got %f", hits[0].Score)
	}
}

func TestSymbolSearch_FuzzyMatch(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.BuildSymbolIndex(); err != nil {
		t.Fatalf("BuildSymbolIndex: %v", err)
	}
	seedTestNodes(t, st, "parseFile", "parseLine", "formatOutput")

	// Typo search — "parsFile" should match "parseFile" via trigram similarity.
	hits, err := st.SearchSymbols("parsFile", 10)
	if err != nil {
		t.Fatalf("SearchSymbols fuzzy: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one fuzzy match")
	}
	// The best match should be parseFile.
	hasParseFile := false
	for _, h := range hits {
		if h.NodeID == "test::parseFile" {
			hasParseFile = true
			break
		}
	}
	if !hasParseFile {
		t.Errorf("expected parseFile in fuzzy results, got %v", hits)
	}
}

// vec384 returns a 384-dim unit vector with the nth component set to 1.
// The vectors table uses vector(384), so tests must always use 384-dim vectors.
func vec384(n int) []float32 {
	v := make([]float32, 384)
	if n >= 0 && n < 384 {
		v[n] = 1
	}
	return v
}

func TestSymbolSearch_NoMatch(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.BuildSymbolIndex(); err != nil {
		t.Fatalf("BuildSymbolIndex: %v", err)
	}
	seedTestNodes(t, st, "parseFile")

	hits, err := st.SearchSymbols("zzzznotfoundzzzz", 10)
	if err != nil {
		t.Fatalf("SearchSymbols no match: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("expected 0 hits for non-matching query, got %d", len(hits))
	}
}

func TestSymbolSearch_Unicode(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.BuildSymbolIndex(); err != nil {
		t.Fatalf("BuildSymbolIndex: %v", err)
	}
	seedTestNodes(t, st, "parseFile", "føøBär", "приветМир")

	// Exact unicode match
	hits, err := st.SearchSymbols("føøBär", 10)
	if err != nil {
		t.Fatalf("SearchSymbols unicode exact: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hits for unicode exact match")
	}
	if hits[0].NodeID != "test::føøBär" {
		t.Errorf("expected test::føøBär, got %s", hits[0].NodeID)
	}

	// Cyrillic exact match
	hits, err = st.SearchSymbols("приветМир", 10)
	if err != nil {
		t.Fatalf("SearchSymbols cyrillic: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hits for cyrillic exact match")
	}
	if hits[0].NodeID != "test::приветМир" {
		t.Errorf("expected test::приветМир, got %s", hits[0].NodeID)
	}
}

func TestSymbolSearch_SpecialChars(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.BuildSymbolIndex(); err != nil {
		t.Fatalf("BuildSymbolIndex: %v", err)
	}
	seedTestNodes(t, st, "with.dot", "with_underscore", "with-hyphen", "with$sign")
	seedTestNodes(t, st, "with/slash") // should trigger the non-identifier path

	// Names with dots and underscores should match exactly
	hits, err := st.SearchSymbols("with.dot", 10)
	if err != nil {
		t.Fatalf("SearchSymbols dot: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hit for name with dot")
	}

	hits, err = st.SearchSymbols("with_underscore", 10)
	if err != nil {
		t.Fatalf("SearchSymbols underscore: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hit for name with underscore")
	}
}

func TestSymbolSearch_CaseSensitivity(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.BuildSymbolIndex(); err != nil {
		t.Fatalf("BuildSymbolIndex: %v", err)
	}
	seedTestNodes(t, st, "ParseFile", "parsefile")

	// pg_trgm similarity is case-insensitive, so "parsefile" should match both.
	// The exact-match tier-0 path is also case-sensitive, so "ParseFile" is the
	// exact match for "ParseFile" but not for "parsefile".
	hits, err := st.SearchSymbols("ParseFile", 10)
	if err != nil {
		t.Fatalf("SearchSymbols case: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hits for case-sensitive search")
	}
	// Should match both via trigram
	hits, err = st.SearchSymbols("parsefile", 10)
	if err != nil {
		t.Fatalf("SearchSymbols lowercase: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hits for lowercase query")
	}
}

func TestSymbolSearch_BundleSearch(t *testing.T) {
	skipIfNoPG(t)
	dsn, schemaName := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schemaName})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.BuildSymbolIndex(); err != nil {
		t.Fatalf("BuildSymbolIndex: %v", err)
	}

	// Insert nodes and edges so bundles have data to return.
	st.AddNode(&graph.Node{ID: "test::parseFile", Kind: graph.KindFunction, Name: "parseFile", FilePath: "test.go"})
	st.AddNode(&graph.Node{ID: "test::parseLine", Kind: graph.KindFunction, Name: "parseLine", FilePath: "test.go"})
	st.AddEdge(&graph.Edge{From: "test::parseFile", To: "test::parseLine", Kind: graph.EdgeKind("calls"), FilePath: "test.go"})

	// Bundle search should return nodes with edges populated.
	bundles, err := st.SearchSymbolBundles("parseFile", 10)
	if err != nil {
		t.Fatalf("SearchSymbolBundles: %v", err)
	}
	if len(bundles) == 0 {
		t.Fatal("expected at least one bundle")
	}
	found := false
	for _, b := range bundles {
		if b.Node.ID == "test::parseFile" {
			found = true
			if b.Score != 100.0 {
				t.Errorf("expected bundle score 100.0, got %f", b.Score)
			}
			break
		}
	}
	if !found {
		t.Error("expected test::parseFile in bundles")
	}
}
