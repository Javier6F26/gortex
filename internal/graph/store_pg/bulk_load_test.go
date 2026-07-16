package store_pg_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/graph/store_pg"
)

// bulkFixture builds a deterministic node/edge set with distinct ids and
// qual_names (so the UNIQUE nodes_by_qual index never collides) and a mix of
// edge keys (a handful collide → exercise dedup). Input order is intentionally
// not key-sorted.
func bulkFixturePG(nNodes, nEdges int) ([]*graph.Node, []*graph.Edge) {
	nodes := make([]*graph.Node, 0, nNodes)
	for i := range nNodes {
		nodes = append(nodes, &graph.Node{
			ID:         fmt.Sprintf("pkg/f%d.go::Sym%d", i%64, i),
			Kind:       graph.KindFunction,
			Name:       fmt.Sprintf("Sym%d", i),
			QualName:   fmt.Sprintf("pkg.f%d.Sym%d", i%64, i),
			FilePath:   fmt.Sprintf("pkg/f%d.go", i%64),
			RepoPrefix: "gortex",
			Language:   "go",
		})
	}
	edges := make([]*graph.Edge, 0, nEdges)
	for i := range nEdges {
		from := nodes[i%nNodes]
		to := nodes[(i*7+1)%nNodes]
		edges = append(edges, &graph.Edge{
			From:       from.ID,
			To:         to.ID,
			Kind:       graph.EdgeCalls,
			FilePath:   from.FilePath,
			Line:       i % 500,
			Confidence: 1,
		})
	}
	return nodes, edges
}

// TestBulkLoadPersistSpeed measures the speedup of bulk-load fast path vs
// normal additive writes. The speedup ratio is gated behind GORTEX_BULK_PERF_ASSERT
// so the default run stays deterministic on noisy CI.
func TestBulkLoadPersistSpeed(t *testing.T) {
	skipIfNoPG(t)
	if testing.Short() {
		t.Skip("skipping persist-speed timing in -short")
	}

	dsn, schemaA := createTestSchema(t)
	// Create a second schema for warm-start baseline (non-destructive merge path).
	_, schemaB := createTestSchema(t)

	n, e := 8000, 16000
	nodes, edges := bulkFixturePG(n, e)

	// PLAIN PATH: normal additive AddBatch without bulk mode.
	stPlain := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schemaA})
	t0 := time.Now()
	stPlain.AddBatch(nodes, edges)
	plainDur := time.Since(t0)
	_ = stPlain.Close()

	// BULK PATH: fast destructive swap (empty → full).
	stBulk := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schemaB})
	t1 := time.Now()
	stBulk.BeginBulkLoad("gortex")
	stBulk.AddBatch(nodes, edges)
	if err := stBulk.FlushBulk("gortex"); err != nil {
		t.Fatalf("FlushBulk: %v", err)
	}
	bulkDur := time.Since(t1)
	_ = stBulk.Close()

	ratio := float64(plainDur) / float64(bulkDur)
	t.Logf("bulk-load persist: plain=%v, bulk=%v, speedup=%.2fx",
		plainDur, bulkDur, ratio)

	// Schema size delta (with file_blobs enabled).
	schemaASize := schemaSizeMB(t, dsn, schemaA)
	schemaBSize := schemaSizeMB(t, dsn, schemaB)
	t.Logf("schema size: plain=%v MB, bulk=%v MB, delta=%v MB",
		schemaASize, schemaBSize, schemaBSize-schemaASize)

	// Assert speedup when GORTEX_BULK_PERF_ASSERT is set. The assertion is
	// deliberately not enforced by default (too noisy on CI).
	if os.Getenv("GORTEX_BULK_PERF_ASSERT") != "" && ratio < 2.0 {
		t.Fatalf("fast path speedup %.2fx below 2x target", ratio)
	}
}

// schemaSizeMB returns the total size in MB of a schema (all tables + indexes).
func schemaSizeMB(t *testing.T, dsn, schema string) float64 {
	t.Helper()
	conn, err := testRootPool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire conn: %v", err)
	}
	defer conn.Release()

	var sizeBytes int64
	q := fmt.Sprintf(`
		SELECT COALESCE(SUM(pg_total_relation_size(schemaname||'.'||tablename)), 0)
		FROM pg_tables
		WHERE schemaname = '%s'
	`, schema)
	if err := conn.QueryRow(context.Background(), q).Scan(&sizeBytes); err != nil {
		t.Fatalf("query schema size: %v", err)
	}
	return float64(sizeBytes) / (1024 * 1024)
}
