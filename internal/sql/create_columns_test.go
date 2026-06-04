package sql

import "testing"

func colNames(cols []CreateColumn) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Name
	}
	return out
}

func eqStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestExtractCreateTablesWithColumns(t *testing.T) {
	ddl := `
CREATE TABLE public.users (
    id bigint NOT NULL,
    email text NOT NULL,
    balance NUMERIC(10,2),
    PRIMARY KEY (id)
);

CREATE TABLE IF NOT EXISTS "events" (
    "id" BIGSERIAL PRIMARY KEY,
    name VARCHAR(255),
    CONSTRAINT uq_name UNIQUE (name),
    FOREIGN KEY (id) REFERENCES users (id)
);
`
	tables := ExtractCreateTablesWithColumns(ddl)
	if len(tables) != 2 {
		t.Fatalf("tables = %d, want 2: %+v", len(tables), tables)
	}

	byName := map[string]CreateTableDef{}
	for _, tb := range tables {
		byName[tb.Table] = tb
	}

	users, ok := byName["users"]
	if !ok {
		t.Fatal("users table not extracted")
	}
	if users.Schema != "public" {
		t.Errorf("users schema = %q, want public", users.Schema)
	}
	if got := colNames(users.Columns); !eqStrs(got, []string{"id", "email", "balance"}) {
		t.Errorf("users columns = %v, want [id email balance] (PRIMARY KEY skipped)", got)
	}
	// The inner comma of NUMERIC(10,2) must not split the column entry.
	for _, c := range users.Columns {
		if c.Name == "balance" && c.Type != "NUMERIC(10,2)" {
			t.Errorf("balance type = %q, want NUMERIC(10,2)", c.Type)
		}
	}

	events := byName["events"]
	if got := colNames(events.Columns); !eqStrs(got, []string{"id", "name"}) {
		t.Errorf("events columns = %v, want [id name] (CONSTRAINT/FOREIGN KEY skipped)", got)
	}
}

func TestIsGeneratedSchema_AndDialect(t *testing.T) {
	ddl := sampleLiveSchema().ToDDL()
	if !IsGeneratedSchema([]byte(ddl)) {
		t.Error("ToDDL output must be recognised as generated schema")
	}
	if d := GeneratedSchemaDialect([]byte(ddl)); d != "postgres" {
		t.Errorf("dialect = %q, want postgres", d)
	}
	if IsGeneratedSchema([]byte("CREATE TABLE x (id int);")) {
		t.Error("plain DDL must not be flagged as generated")
	}
	if d := GeneratedSchemaDialect([]byte("CREATE TABLE x (id int);")); d != "" {
		t.Errorf("non-generated content dialect = %q, want empty", d)
	}
}

func TestGeneratedSchema_RoundTripsColumns(t *testing.T) {
	// The point of NEW-GFY-8: a live schema's columns survive the DDL
	// round-trip into the column extractor.
	ddl := sampleLiveSchema().ToDDL()
	tables := ExtractCreateTablesWithColumns(ddl)
	cols := map[string][]string{}
	for _, tb := range tables {
		cols[tb.Table] = colNames(tb.Columns)
	}
	if !eqStrs(cols["users"], []string{"id", "email"}) {
		t.Errorf("users columns = %v, want [id email]", cols["users"])
	}
	if !eqStrs(cols["orders"], []string{"id", "user_id"}) {
		t.Errorf("orders columns = %v, want [id user_id]", cols["orders"])
	}
}
