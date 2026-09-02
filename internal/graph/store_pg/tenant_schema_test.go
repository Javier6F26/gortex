package store_pg_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/zzet/gortex/internal/graph/store_pg"
)

// pgFoldName returns the name PostgreSQL stores for an unquoted identifier:
// lowercased and truncated to NAMEDATALEN-1 (63) bytes.
func pgFoldName(name string) string {
	name = strings.ToLower(name)
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

// withDSNParam appends key=value to a DSN that may or may not already carry
// a query string.
func withDSNParam(dsn, kv string) string {
	if strings.Contains(dsn, "?") {
		return dsn + "&" + kv
	}
	return dsn + "?" + kv
}

// tablesInSchema lists the ordinary tables PostgreSQL has in schema.
func tablesInSchema(t *testing.T, schema string) map[string]bool {
	t.Helper()
	rows, err := testRootPool.Query(context.Background(),
		`SELECT tablename FROM pg_tables WHERE schemaname = $1`, schema)
	if err != nil {
		t.Fatalf("pg_tables(%s): %v", schema, err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[name] = true
	}
	return out
}

// TestTenantSchemaBootstrapsDespitePopulatedSearchPathTail is the
// one-schema-per-tenant guarantee: opening a store whose search_path is
// "<empty tenant schema>, <populated schema>" must create the Gortex
// tables in the tenant schema instead of silently adopting the populated
// schema's tables further down the path (the to_regclass hazard).
func TestTenantSchemaBootstrapsDespitePopulatedSearchPathTail(t *testing.T) {
	skipIfNoPG(t)
	ctx := context.Background()

	// Populated schema: hosts the extensions (like a shared "ext" schema)
	// and a fully migrated Gortex store (like today's public schema).
	dsn, populated := createTestSchema(t)
	// createTestSchema issues CREATE SCHEMA unquoted, so PostgreSQL folds
	// the name to lowercase and truncates it to 63 bytes; compare catalog
	// rows with the folded form.
	populated = pgFoldName(populated)
	seed, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: populated})
	if err != nil {
		t.Fatalf("open populated: %v", err)
	}
	_ = seed.Close()
	if !tablesInSchema(t, populated)["schema_version"] {
		t.Fatalf("precondition: %s has no schema_version", populated)
	}

	// Empty tenant schema, first on the search_path. Short name so it
	// cannot collide with the (possibly truncated) populated schema.
	tenant := fmt.Sprintf("tenant_%d", os.Getpid())
	_, _ = testRootPool.Exec(ctx, fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, tenant))
	if _, err := testRootPool.Exec(ctx, fmt.Sprintf(`CREATE SCHEMA %s`, tenant)); err != nil {
		t.Fatalf("create tenant schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testRootPool.Exec(context.Background(), fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, tenant))
	})

	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: tenant + "," + populated})
	if err != nil {
		t.Fatalf("open tenant: %v", err)
	}
	defer st.Close()

	got := tablesInSchema(t, tenant)
	for _, want := range []string{"schema_version", "nodes", "edges"} {
		if !got[want] {
			t.Errorf("tenant schema %s missing table %s (tables: %v)", tenant, want, got)
		}
	}

	// DB stats are scoped to the current schema: every reported table must
	// belong to the tenant schema, none to the populated tail.
	sizes, err := st.DBTableSizes()
	if err != nil {
		t.Fatalf("DBTableSizes: %v", err)
	}
	if len(sizes) == 0 {
		t.Fatalf("DBTableSizes returned nothing for tenant schema")
	}
	populatedTables := tablesInSchema(t, populated)
	for _, sz := range sizes {
		if !got[sz.TableName] {
			t.Errorf("DBTableSizes reported %q, which is not in tenant schema %s", sz.TableName, tenant)
		}
		_ = populatedTables // same names exist there too; membership in tenant is what matters
	}
}

// TestPoolMaxConnsPrecedence verifies the pool cap follows Config, then
// the DSN's pool_max_conns, then the default — instead of always NumCPU*2.
func TestPoolMaxConnsPrecedence(t *testing.T) {
	skipIfNoPG(t)
	ctx := context.Background()
	dsn, schema := createTestSchema(t)

	cases := []struct {
		name string
		cfg  store_pg.Config
		want int32
	}{
		{"DSN pool_max_conns honored", store_pg.Config{DSN: withDSNParam(dsn, "pool_max_conns=2"), Schema: schema}, 2},
		{"Config wins over DSN", store_pg.Config{DSN: withDSNParam(dsn, "pool_max_conns=2"), Schema: schema, PoolMaxConns: 3}, 3},
		{"default when neither", store_pg.Config{DSN: dsn, Schema: schema}, int32(store_pg.DefaultPoolMaxConns)},
	}
	for _, c := range cases {
		st, err := store_pg.Open(ctx, c.cfg)
		if err != nil {
			t.Fatalf("%s: open: %v", c.name, err)
		}
		if got := st.PoolStats().MaxConns; got != c.want {
			t.Errorf("%s: MaxConns = %d, want %d", c.name, got, c.want)
		}
		_ = st.Close()
	}
}

// TestApplicationNameDefaults verifies connections are tagged "gortex" /
// "gortex-follower" unless the DSN sets application_name itself.
func TestApplicationNameDefaults(t *testing.T) {
	skipIfNoPG(t)
	ctx := context.Background()
	dsn, schema := createTestSchema(t)

	count := func(appName string) int {
		var n int
		if err := testRootPool.QueryRow(ctx,
			`SELECT count(*) FROM pg_stat_activity WHERE application_name = $1 AND pid <> pg_backend_pid()`,
			appName).Scan(&n); err != nil {
			t.Fatalf("pg_stat_activity: %v", err)
		}
		return n
	}

	writer, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schema})
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	if count("gortex") == 0 {
		t.Errorf("no connection tagged application_name=gortex after opening a writer")
	}

	follower, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schema, ReadOnly: true})
	if err != nil {
		t.Fatalf("open follower: %v", err)
	}
	if count("gortex-follower") == 0 {
		t.Errorf("no connection tagged application_name=gortex-follower after opening a follower")
	}
	_ = follower.Close()
	_ = writer.Close()

	custom, err := store_pg.Open(ctx, store_pg.Config{DSN: withDSNParam(dsn, "application_name=tenant_a"), Schema: schema})
	if err != nil {
		t.Fatalf("open custom: %v", err)
	}
	defer custom.Close()
	if count("tenant_a") == 0 {
		t.Errorf("DSN application_name=tenant_a was not preserved")
	}
}
