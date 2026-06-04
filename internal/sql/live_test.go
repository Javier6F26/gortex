package sql

import (
	"strings"
	"testing"
)

func sampleLiveSchema() *LiveSchema {
	return &LiveSchema{
		Dialect: "postgres",
		Columns: []LiveColumn{
			{Schema: "public", Table: "users", Name: "id", DataType: "bigint", Nullable: false, Ordinal: 1, IsPrimaryKey: true},
			{Schema: "public", Table: "users", Name: "email", DataType: "text", Nullable: false, Ordinal: 2},
			{Schema: "public", Table: "orders", Name: "id", DataType: "bigint", Nullable: false, Ordinal: 1, IsPrimaryKey: true},
			{Schema: "public", Table: "orders", Name: "user_id", DataType: "bigint", Nullable: true, Ordinal: 2},
		},
		ForeignKeys: []LiveForeignKey{
			{Schema: "public", Table: "orders", Column: "user_id", RefSchema: "public", RefTable: "users", RefColumn: "id"},
		},
	}
}

func TestLiveSchema_ToDDL(t *testing.T) {
	ddl := sampleLiveSchema().ToDDL()

	for _, want := range []string{
		"CREATE TABLE orders (",
		"CREATE TABLE users (",
		"id bigint NOT NULL",
		"email text NOT NULL",
		"PRIMARY KEY (id)",
		"ALTER TABLE orders ADD FOREIGN KEY (user_id) REFERENCES users (id);",
	} {
		if !strings.Contains(ddl, want) {
			t.Errorf("DDL missing %q\n---\n%s", want, ddl)
		}
	}
}

func TestLiveSchema_ToDDL_RoundTripsThroughExtractor(t *testing.T) {
	// The whole point of emitting DDL is that the existing SQL extractor
	// turns it into the same table nodes as a migration would.
	ddl := sampleLiveSchema().ToDDL()
	tables := ExtractCreateTables(ddl)
	got := map[string]bool{}
	for _, tr := range tables {
		got[tr.Table] = true
		if tr.Op != "create" {
			t.Errorf("table %s op = %q, want create", tr.Table, tr.Op)
		}
	}
	for _, want := range []string{"users", "orders"} {
		if !got[want] {
			t.Errorf("ExtractCreateTables did not recover table %q from generated DDL (got %v)", want, got)
		}
	}

	// The generated table IDs match the canonical db::postgres:: scheme.
	if id := TableNodeID("postgres", "", "users"); id != "db::postgres::users" {
		t.Errorf("unexpected table node id %q", id)
	}
}

func TestLiveSchema_ToDDL_NonPublicSchemaQualified(t *testing.T) {
	ls := &LiveSchema{
		Dialect: "postgres",
		Columns: []LiveColumn{
			{Schema: "billing", Table: "invoices", Name: "id", DataType: "uuid", Nullable: false, Ordinal: 1, IsPrimaryKey: true},
		},
	}
	ddl := ls.ToDDL()
	if !strings.Contains(ddl, "CREATE TABLE billing.invoices (") {
		t.Errorf("non-public schema should be qualified: %s", ddl)
	}
}
