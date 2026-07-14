package store_pg_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/graph/store_pg"
)

// TestCrashDurability is a two-phase manual durability check (change task
// 5.3), gated on GORTEX_CRASH_PHASE so it never runs in the normal suite.
//
//	GORTEX_CRASH_PHASE=seed   — cold-index into a fixed schema (destructive
//	                            swap → LOGGED tables), force a synchronous
//	                            WAL flush, print the row counts.
//	<crash + restart PG immediately: docker kill -s SIGKILL … ; docker start …>
//	GORTEX_CRASH_PHASE=verify — reopen the same schema and assert the row
//	                            counts survived crash recovery.
//
// UNLOGGED live tables (the pre-change behavior) are truncated on unclean
// restart, so verify would see 0; LOGGED tables survive.
const crashSchema = "crash_durability"

func TestCrashDurability(t *testing.T) {
	phase := os.Getenv("GORTEX_CRASH_PHASE")
	if phase == "" {
		t.Skip("set GORTEX_CRASH_PHASE=seed|verify to run the crash durability check")
	}
	skipIfNoPG(t)
	ctx := context.Background()

	switch phase {
	case "seed":
		// Fresh schema without a drop-cleanup so it survives the crash.
		_, _ = testRootPool.Exec(ctx, `DROP SCHEMA IF EXISTS `+crashSchema+` CASCADE`)
		if _, err := testRootPool.Exec(ctx, `CREATE SCHEMA `+crashSchema); err != nil {
			t.Fatalf("create schema: %v", err)
		}
		_, _ = testRootPool.Exec(ctx, `DROP EXTENSION IF EXISTS pg_trgm CASCADE`)
		_, _ = testRootPool.Exec(ctx, `DROP EXTENSION IF EXISTS vector CASCADE`)
		_, _ = testRootPool.Exec(ctx, `CREATE EXTENSION pg_trgm WITH SCHEMA `+crashSchema)
		_, _ = testRootPool.Exec(ctx, `CREATE EXTENSION vector WITH SCHEMA `+crashSchema)

		st, err := store_pg.Open(ctx, store_pg.Config{DSN: testPGDSN, Schema: crashSchema})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		var nodes []*graph.Node
		var edges []*graph.Edge
		for i := 0; i < 1000; i++ {
			nodes = append(nodes, hnode("repoA", fmt.Sprintf("repoA/a.go::N%d", i), fmt.Sprintf("N%d", i)))
			if i > 0 {
				edges = append(edges, hedge(fmt.Sprintf("repoA/a.go::N%d", i), "repoA/a.go::N0"))
			}
		}
		bulkLoad(t, st, "repoA", nodes, edges) // destructive swap → LOGGED
		// One synchronous write flushes the WAL up to and including the
		// async-committed bulk swap, so an immediate crash cannot lose it.
		st.AddNode(hnode("repoA", "repoA/a.go::Sync", "Sync"))
		t.Logf("seeded: nodes=%d edges=%d", st.NodeCount(), st.EdgeCount())
		_ = st.Close()

	case "verify":
		st, err := store_pg.Open(ctx, store_pg.Config{DSN: testPGDSN, Schema: crashSchema})
		if err != nil {
			t.Fatalf("reopen after crash: %v", err)
		}
		defer st.Close()
		n, e := st.NodeCount(), st.EdgeCount()
		t.Logf("after crash recovery: nodes=%d edges=%d", n, e)
		if n != 1001 {
			t.Errorf("NodeCount = %d, want 1001 (LOGGED tables must survive crash recovery)", n)
		}
		if e != 999 {
			t.Errorf("EdgeCount = %d, want 999 (LOGGED tables must survive crash recovery)", e)
		}

	default:
		t.Fatalf("unknown GORTEX_CRASH_PHASE=%q", phase)
	}
}

// ---------------------------------------------------------------------------
// Shared helpers for the harden-pg-store tests.
// ---------------------------------------------------------------------------

func openHardenStore(t *testing.T, cfg store_pg.Config) *store_pg.Store {
	t.Helper()
	st, err := store_pg.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func hnode(repo, id, name string) *graph.Node {
	return &graph.Node{
		ID: id, Kind: graph.KindFunction, Name: name,
		FilePath: repo + "/f.go", RepoPrefix: repo, Language: "go",
	}
}

func hedge(from, to string) *graph.Edge {
	return &graph.Edge{From: from, To: to, Kind: graph.EdgeCalls, Confidence: 1.0}
}

// bulkLoad runs one BeginBulkLoad/AddBatchBulk/FlushBulk cycle for a repo.
func bulkLoad(t *testing.T, st *store_pg.Store, repo string, nodes []*graph.Node, edges []*graph.Edge) {
	t.Helper()
	st.BeginBulkLoad(repo)
	st.AddBatchBulk(nodes, edges)
	if err := st.FlushBulk(repo); err != nil {
		t.Fatalf("FlushBulk(%s): %v", repo, err)
	}
}

// relpersistence returns pg_class.relpersistence for a table in the schema.
func relpersistence(t *testing.T, schema, table string) string {
	t.Helper()
	var p string
	err := testRootPool.QueryRow(context.Background(),
		`SELECT c.relpersistence FROM pg_class c
		 JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = left(lower($1), 63) AND c.relname = $2`, schema, table).Scan(&p)
	if err != nil {
		t.Fatalf("relpersistence(%s.%s): %v", schema, table, err)
	}
	return p
}

// ---------------------------------------------------------------------------
// 1.7 — Bulk load: LOGGED tables, leftover sweep, merge-path stale deletes.
// ---------------------------------------------------------------------------

// Destructive swap must leave the live tables LOGGED (relpersistence 'p')
// so the graph survives crash recovery and ships to physical replicas.
func TestBulkLoad_DestructiveSwapProducesLoggedTables(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema := createTestSchema(t)
	st := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schema})

	bulkLoad(t, st, "repoA",
		[]*graph.Node{hnode("repoA", "repoA/a.go::Foo", "Foo"), hnode("repoA", "repoA/a.go::Bar", "Bar")},
		[]*graph.Edge{hedge("repoA/a.go::Foo", "repoA/a.go::Bar")})

	if got := relpersistence(t, schema, "nodes"); got != "p" {
		t.Errorf("nodes relpersistence = %q, want \"p\" (LOGGED)", got)
	}
	if got := relpersistence(t, schema, "edges"); got != "p" {
		t.Errorf("edges relpersistence = %q, want \"p\" (LOGGED)", got)
	}
	if n := st.NodeCount(); n != 2 {
		t.Errorf("NodeCount = %d, want 2", n)
	}
}

// A committed leftover staging table (as a crashed writer might strand)
// must not poison a new flush, and the leftover sweep must drop it.
func TestBulkLoad_LeftoverStagingSweep(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema := createTestSchema(t)
	st := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schema})
	// Seed the schema so relations exist for LIKE.
	bulkLoad(t, st, "repoA", []*graph.Node{hnode("repoA", "repoA/a.go::Foo", "Foo")}, nil)

	// Strand a leftover staging table matching the pattern.
	leftover := "nodes_bulk_999999_1"
	if _, err := testRootPool.Exec(context.Background(),
		fmt.Sprintf(`CREATE TABLE %s.%s (id text)`, schema, leftover)); err != nil {
		t.Fatalf("create leftover: %v", err)
	}

	// A new bulk cycle triggers BeginBulkLoad's sweep and must succeed.
	bulkLoad(t, st, "repoB", []*graph.Node{hnode("repoB", "repoB/b.go::Baz", "Baz")}, nil)

	var exists bool
	if err := testRootPool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		 WHERE n.nspname=left(lower($1),63) AND c.relname=$2)`, schema, leftover).Scan(&exists); err != nil {
		t.Fatalf("check leftover: %v", err)
	}
	if exists {
		t.Errorf("leftover staging table %q was not swept", leftover)
	}
}

// The non-destructive merge path must fully REPLACE a repo on re-index:
// upsert live rows AND delete rows that vanished from the new index.
func TestBulkLoad_MergePathDeletesStaleRows(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema := createTestSchema(t)
	st := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schema})

	// repoA first (destructive, empties the store onto LOGGED tables).
	bulkLoad(t, st, "repoA", []*graph.Node{hnode("repoA", "repoA/a.go::Foo", "Foo")}, nil)

	// repoB via the merge path with two nodes + an edge between them.
	bulkLoad(t, st, "repoB",
		[]*graph.Node{
			hnode("repoB", "repoB/b.go::One", "One"),
			hnode("repoB", "repoB/b.go::Two", "Two"),
		},
		[]*graph.Edge{hedge("repoB/b.go::One", "repoB/b.go::Two")})

	if n := len(st.GetRepoNodes("repoB")); n != 2 {
		t.Fatalf("after first repoB index: GetRepoNodes = %d, want 2", n)
	}
	if e := len(st.GetRepoEdges("repoB")); e != 1 {
		t.Fatalf("after first repoB index: GetRepoEdges = %d, want 1", e)
	}

	// Re-index repoB with only "One" and no edges: "Two" and the edge
	// vanished from the new index and must be deleted.
	bulkLoad(t, st, "repoB", []*graph.Node{hnode("repoB", "repoB/b.go::One", "One")}, nil)

	nodes := st.GetRepoNodes("repoB")
	if len(nodes) != 1 || nodes[0].ID != "repoB/b.go::One" {
		t.Errorf("after re-index: GetRepoNodes = %+v, want just repoB/b.go::One", nodes)
	}
	if e := len(st.GetRepoEdges("repoB")); e != 0 {
		t.Errorf("after re-index: stale edge not deleted, GetRepoEdges = %d, want 0", e)
	}
	// repoA must be untouched by repoB's merge.
	if n := len(st.GetRepoNodes("repoA")); n != 1 {
		t.Errorf("repoA disturbed by repoB merge: GetRepoNodes = %d, want 1", n)
	}
}

// Re-indexing an identical repo through the merge path must be idempotent.
func TestBulkLoad_MergeIdempotent(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema := createTestSchema(t)
	st := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schema})

	bulkLoad(t, st, "repoA", []*graph.Node{hnode("repoA", "repoA/a.go::Foo", "Foo")}, nil)
	nodes := []*graph.Node{
		hnode("repoB", "repoB/b.go::One", "One"),
		hnode("repoB", "repoB/b.go::Two", "Two"),
	}
	edges := []*graph.Edge{hedge("repoB/b.go::One", "repoB/b.go::Two")}
	bulkLoad(t, st, "repoB", nodes, edges)
	bulkLoad(t, st, "repoB", nodes, edges) // second identical index

	if n := len(st.GetRepoNodes("repoB")); n != 2 {
		t.Errorf("GetRepoNodes = %d, want 2", n)
	}
	if e := len(st.GetRepoEdges("repoB")); e != 1 {
		t.Errorf("GetRepoEdges = %d, want 1 (no duplicates)", e)
	}
}

// ---------------------------------------------------------------------------
// 2.6 — Read resilience: no panic, degrade + health, retry-then-succeed.
// ---------------------------------------------------------------------------

// A read against a hard-failed store degrades to a zero value and flips
// the health flag — it never panics.
func TestReadDegradesWithoutPanic(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schema})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	st.AddNode(hnode("repoA", "repoA/a.go::Foo", "Foo"))

	// Close the store: reads now run on a cancelled ctx / closed pool.
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// None of these must panic; each degrades to its zero value.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("read panicked after store failure: %v", r)
			}
		}()
		_ = st.GetNode("repoA/a.go::Foo")
		_ = st.AllNodes()
		_ = st.NodeCount()
		_ = st.AllEdges()
	}()

	if h := st.Health(); h.DegradedReads == 0 {
		t.Errorf("Health().DegradedReads = 0, want > 0 after degraded reads")
	} else if h.LastError == "" {
		t.Errorf("Health().LastError empty, want the last degraded-read error")
	}
}

// Killing the backend connection is a transient (retryable) fault: the
// store retries, reconnects, and returns the correct result — no panic.
func TestReadRetriesThroughBackendTermination(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema := createTestSchema(t)
	appName := "harden_retry_" + sanitizeSchemaName(t.Name())
	cfg := store_pg.Config{
		DSN:          dsn + "&application_name=" + appName,
		Schema:       schema,
		PoolMaxConns: 2,
		PoolMinConns: 1,
	}
	st := openHardenStore(t, cfg)

	for i := 0; i < 5; i++ {
		st.AddNode(hnode("repoA", fmt.Sprintf("repoA/a.go::N%d", i), fmt.Sprintf("N%d", i)))
	}
	if n := st.NodeCount(); n != 5 {
		t.Fatalf("baseline NodeCount = %d, want 5", n)
	}

	// Terminate every backend belonging to this store.
	if _, err := testRootPool.Exec(context.Background(),
		`SELECT pg_terminate_backend(pid) FROM pg_stat_activity
		 WHERE application_name = $1 AND pid <> pg_backend_pid()`, appName); err != nil {
		t.Fatalf("pg_terminate_backend: %v", err)
	}

	var got int
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("read panicked after backend termination: %v", r)
			}
		}()
		got = st.NodeCount()
	}()
	if got != 5 {
		t.Errorf("NodeCount after backend kill = %d, want 5 (retry should reconnect)", got)
	}
}

// ---------------------------------------------------------------------------
// 3.6 — Schema safety: concurrent Opens, version-read error, read-only mode.
// ---------------------------------------------------------------------------

// N processes opening an empty schema concurrently: exactly one migrates,
// the rest block on the advisory lock and no-op. None fail with 42P07.
func TestSchema_ConcurrentOpensNoRace(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema := createTestSchema(t)
	ctx := context.Background()

	const n = 6
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schema})
			if err != nil {
				errs[idx] = err
				return
			}
			_ = st.Close()
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent Open[%d] failed: %v", i, err)
		}
	}
}

// A version-read failure (malformed schema_version) must fail Open and
// must NOT run any DDL.
func TestSchema_VersionReadErrorFailsOpenNoDDL(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema := createTestSchema(t)
	ctx := context.Background()

	// A schema_version table without a `version` column makes the
	// MAX(version) probe fail (undefined_column), which must propagate.
	if _, err := testRootPool.Exec(ctx,
		fmt.Sprintf(`CREATE TABLE %s.schema_version (foo int)`, schema)); err != nil {
		t.Fatalf("create malformed schema_version: %v", err)
	}

	_, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schema})
	if err == nil {
		t.Fatal("Open succeeded, want version-read error")
	}

	// No DDL should have run: the nodes table must not exist.
	var exists bool
	if err := testRootPool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		 WHERE n.nspname=left(lower($1),63) AND c.relname='nodes')`, schema).Scan(&exists); err != nil {
		t.Fatalf("check nodes: %v", err)
	}
	if exists {
		t.Error("nodes table was created despite version-read error")
	}
}

// Read-only Open succeeds against a current schema and serves reads and
// read capabilities, but refuses every write.
func TestSchema_ReadOnlyOpenAndWriteGuard(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema := createTestSchema(t)
	ctx := context.Background()

	// Writer migrates + seeds, then closes.
	wr := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schema})
	wr.AddNode(hnode("repoA", "repoA/a.go::Foo", "Foo"))
	if err := wr.Close(); err != nil {
		t.Fatalf("writer Close: %v", err)
	}

	ro, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schema, ReadOnly: true})
	if err != nil {
		t.Fatalf("read-only Open on current schema: %v", err)
	}
	t.Cleanup(func() { _ = ro.Close() })

	// Reads serve the writer's data.
	if n := ro.NodeCount(); n != 1 {
		t.Errorf("read-only NodeCount = %d, want 1", n)
	}

	// Read capabilities still assert.
	if _, ok := interface{}(ro).(graph.ContentSearcher); !ok {
		t.Error("read-only store lost ContentSearcher capability")
	}
	if _, ok := interface{}(ro).(graph.VectorSearcher); !ok {
		t.Error("read-only store lost VectorSearcher capability")
	}
	if _, ok := interface{}(ro).(graph.BFSCapable); !ok {
		t.Error("read-only store lost BFSCapable capability")
	}

	// Error-returning mutators return ErrReadOnlyStore.
	errWrites := map[string]func() error{
		"SetRepoIndexState":    func() error { return ro.SetRepoIndexState(graph.RepoIndexState{}) },
		"AppendContent":        func() error { return ro.AppendContent("repoA", nil) },
		"BulkUpsertEmbeddings": func() error { return ro.BulkUpsertEmbeddings(nil) },
		"BulkSetBlame":         func() error { return ro.BulkSetBlame("repoA", nil) },
		"BuildContentIndex":    func() error { return ro.BuildContentIndex() },
		"FlushBulk":            func() error { ro.BeginBulkLoad("repoA"); return ro.FlushBulk("repoA") },
	}
	for name, fn := range errWrites {
		if err := fn(); !errors.Is(err, store_pg.ErrReadOnlyStore) {
			t.Errorf("%s on read-only store = %v, want ErrReadOnlyStore", name, err)
		}
	}

	// Void mutators drop-and-record: they must bump WriteRefusals.
	before := ro.Health().WriteRefusals
	ro.AddNode(hnode("repoA", "repoA/a.go::New", "New"))
	ro.AddBatch([]*graph.Node{hnode("repoA", "repoA/a.go::New2", "New2")}, nil)
	ro.EvictFile("repoA/f.go")
	if after := ro.Health().WriteRefusals; after <= before {
		t.Errorf("WriteRefusals = %d, want > %d after void writes", after, before)
	}

	// No write reached PostgreSQL.
	if n := ro.NodeCount(); n != 1 {
		t.Errorf("read-only NodeCount after refused writes = %d, want 1", n)
	}
}

// Read-only Open against an outdated schema fails with a typed mismatch
// error and runs no migration.
func TestSchema_ReadOnlyOutdatedFailsTyped(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema := createTestSchema(t)
	ctx := context.Background()

	// Fresh (unmigrated) schema is at version 0 < expected.
	_, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schema, ReadOnly: true})
	if !errors.Is(err, store_pg.ErrSchemaVersionMismatch) {
		t.Fatalf("read-only Open on blank schema = %v, want ErrSchemaVersionMismatch", err)
	}
	var mism *store_pg.SchemaVersionMismatchError
	if !errors.As(err, &mism) {
		t.Fatalf("error not a *SchemaVersionMismatchError: %v", err)
	}
	if mism.Stored != 0 {
		t.Errorf("mismatch Stored = %d, want 0", mism.Stored)
	}

	// No DDL: nodes must not exist.
	var exists bool
	if err := testRootPool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		 WHERE n.nspname=left(lower($1),63) AND c.relname='nodes')`, schema).Scan(&exists); err != nil {
		t.Fatalf("check nodes: %v", err)
	}
	if exists {
		t.Error("read-only Open created tables on an outdated schema")
	}
}
