package store_pg_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/graph/store_pg"
)

// dupQualNameNodes builds nodes that intentionally share a qual_name across
// distinct ids — the shape produced by branch/worktree copies of the same tree
// and by generated/repeated code. Before the fix this collided on the unique
// idx_nodes_qual_name (SQLSTATE 23505).
func dupQualNameNodes() []*graph.Node {
	return []*graph.Node{
		{ID: "branchA/pkg/f.go::Foo", Kind: graph.KindFunction, Name: "Foo", QualName: "pkg.Foo", FilePath: "branchA/pkg/f.go", RepoPrefix: "repo", Language: "go"},
		{ID: "branchB/pkg/f.go::Foo", Kind: graph.KindFunction, Name: "Foo", QualName: "pkg.Foo", FilePath: "branchB/pkg/f.go", RepoPrefix: "repo", Language: "go"},
		{ID: "branchC/pkg/f.go::Foo", Kind: graph.KindFunction, Name: "Foo", QualName: "pkg.Foo", FilePath: "branchC/pkg/f.go", RepoPrefix: "repo", Language: "go"},
		{ID: "pkg/g.go::Bar", Kind: graph.KindFunction, Name: "Bar", QualName: "pkg.Bar", FilePath: "pkg/g.go", RepoPrefix: "repo", Language: "go"},
	}
}

// countNodesByQualName counts persisted nodes for a qual_name directly against
// the test schema, so the assertion verifies real rows survived the merge
// (not a LIMIT-1 store lookup that would hide duplicate loss).
func countNodesByQualName(t *testing.T, schema, qualName string) int {
	t.Helper()
	var n int
	q := fmt.Sprintf(`SELECT count(*) FROM %s.nodes WHERE qual_name = $1`, schema)
	if err := testRootPool.QueryRow(context.Background(), q, qualName).Scan(&n); err != nil {
		t.Fatalf("count nodes by qual_name: %v", err)
	}
	return n
}

// TestBulkLoad_DuplicateQualNames_DestructivePath verifies the cold-swap path
// (empty → full) accepts internally-duplicated qual_names without 23505.
func TestBulkLoad_DuplicateQualNames_DestructivePath(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema := createTestSchema(t)
	st := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schema})
	defer st.Close()

	st.BeginBulkLoad("repo")
	st.AddBatch(dupQualNameNodes(), nil)
	if err := st.FlushBulk("repo"); err != nil {
		t.Fatalf("FlushBulk (destructive path) with duplicate qual_names: %v", err)
	}

	if got := countNodesByQualName(t, schema, "pkg.Foo"); got != 3 {
		t.Errorf("nodes with qual_name pkg.Foo = %d, want 3 (all branch copies preserved)", got)
	}
}

// TestBulkLoad_DuplicateQualNames_SafeMergePath verifies the incremental merge
// path (INSERT … SELECT … ON CONFLICT (id)) — the one that failed in the
// incident ("merge nodes from staging") — accepts duplicate qual_names.
func TestBulkLoad_DuplicateQualNames_SafeMergePath(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema := createTestSchema(t)
	st := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schema})
	defer st.Close()

	// First load populates the live table (destructive), so the second load
	// takes the safe non-destructive merge path.
	st.BeginBulkLoad("seed")
	st.AddBatch([]*graph.Node{
		{ID: "seed/x.go::X", Kind: graph.KindFunction, Name: "X", QualName: "seed.X", FilePath: "seed/x.go", RepoPrefix: "seed", Language: "go"},
	}, nil)
	if err := st.FlushBulk("seed"); err != nil {
		t.Fatalf("FlushBulk (seed): %v", err)
	}

	// Second load carries internally-duplicated qual_names into the safe merge.
	st.BeginBulkLoad("repo")
	st.AddBatch(dupQualNameNodes(), nil)
	if err := st.FlushBulk("repo"); err != nil {
		t.Fatalf("FlushBulk (safe merge path) with duplicate qual_names: %v", err)
	}

	if got := countNodesByQualName(t, schema, "pkg.Foo"); got != 3 {
		t.Errorf("nodes with qual_name pkg.Foo = %d, want 3 (all branch copies preserved)", got)
	}
}

// v6DropByDefinitionSQL mirrors migration V6's core: drop every unique
// single-column index on nodes(qual_name) in the current schema (whatever its
// name — the cold-swap path leaves an auto-named one) and recreate the
// canonical non-unique index. Kept in sync with schema_version.go V6.
const v6DropByDefinitionSQL = `
DO $$
DECLARE idx_name text;
BEGIN
    FOR idx_name IN
        SELECT i.relname
        FROM pg_index x
        JOIN pg_class i ON i.oid = x.indexrelid
        JOIN pg_class t ON t.oid = x.indrelid
        JOIN pg_namespace n ON n.oid = t.relnamespace
        WHERE t.relname = 'nodes'
          AND n.nspname = current_schema()
          AND x.indisunique
          AND x.indnatts = 1
          AND pg_get_indexdef(x.indexrelid) ILIKE '%(qual_name)%'
    LOOP
        EXECUTE format('DROP INDEX IF EXISTS %I.%I', current_schema(), idx_name);
    END LOOP;
END $$;
CREATE INDEX IF NOT EXISTS idx_nodes_qual_name ON nodes(qual_name) WHERE qual_name <> '';`

// TestMigrationV6_DropsAnyNamedUniqueIndex simulates a legacy deployment whose
// qual_name unique index carries a non-canonical (auto-generated) name — the
// state left by the destructive cold-swap path — and verifies V6's
// drop-by-definition logic demotes it, unblocking duplicate qual_names. This is
// the load-bearing path for the incident deployments.
func TestMigrationV6_DropsAnyNamedUniqueIndex(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema := createTestSchema(t)
	st := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schema})
	defer st.Close()
	ctx := context.Background()

	// Simulate the legacy state: replace the non-unique index (from V6 on this
	// fresh store) with a UNIQUE index under an auto-generated-style name, as a
	// post-swap store would carry.
	exec := func(sql string) {
		t.Helper()
		if _, err := testRootPool.Exec(ctx, fmt.Sprintf("SET search_path TO %s; ", schema)+sql); err != nil {
			t.Fatalf("setup exec %q: %v", sql, err)
		}
	}
	exec(`DROP INDEX IF EXISTS idx_nodes_qual_name`)
	exec(`CREATE UNIQUE INDEX nodes_tmp_qual_name_key ON nodes(qual_name) WHERE qual_name <> ''`)

	insertDup := func() error {
		// Full-enough rows so the ONLY possible failure is the qual_name unique
		// violation — not an unrelated NOT NULL — making the "before" assertion
		// meaningful.
		_, err := testRootPool.Exec(ctx, fmt.Sprintf(
			`INSERT INTO %s.nodes (id, kind, name, file_path, qual_name)
			 VALUES ('a','function','Dup','a.go','pkg.Dup'), ('b','function','Dup','b.go','pkg.Dup')`, schema))
		return err
	}

	// Before V6: the unique index rejects duplicate qual_names — the exact
	// SQLSTATE 23505 from the incident, not an unrelated constraint.
	err := insertDup()
	if err == nil {
		t.Fatal("expected a unique-violation before the migration, got nil")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("expected SQLSTATE 23505 (unique_violation) before the migration, got: %v", err)
	}
	// Clean the partial insert so the post-migration insert starts fresh.
	_, _ = testRootPool.Exec(ctx, fmt.Sprintf(`DELETE FROM %s.nodes WHERE qual_name = 'pkg.Dup'`, schema))

	// Apply V6's drop-by-definition logic.
	if _, err := testRootPool.Exec(ctx, fmt.Sprintf("SET search_path TO %s; ", schema)+v6DropByDefinitionSQL); err != nil {
		t.Fatalf("apply V6 logic: %v", err)
	}

	// After V6: duplicate qual_names insert cleanly.
	if err := insertDup(); err != nil {
		t.Fatalf("duplicate qual_names still rejected after V6: %v", err)
	}
	if got := countNodesByQualName(t, schema, "pkg.Dup"); got != 2 {
		t.Errorf("nodes with qual_name pkg.Dup = %d, want 2", got)
	}
}

// TestAddBatch_DuplicateQualNames verifies the plain additive path (live table,
// non-unique index) also tolerates duplicate qual_names.
func TestAddBatch_DuplicateQualNames(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema := createTestSchema(t)
	st := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schema})
	defer st.Close()

	st.AddBatch(dupQualNameNodes(), nil)

	if got := countNodesByQualName(t, schema, "pkg.Foo"); got != 3 {
		t.Errorf("nodes with qual_name pkg.Foo = %d, want 3", got)
	}
}
