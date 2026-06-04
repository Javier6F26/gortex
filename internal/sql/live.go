package sql

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// LiveColumn is a column introspected from a live database's
// information_schema.
type LiveColumn struct {
	Schema       string
	Table        string
	Name         string
	DataType     string
	Nullable     bool
	Ordinal      int
	IsPrimaryKey bool
}

// LiveForeignKey is a foreign-key relationship introspected from a live
// database.
type LiveForeignKey struct {
	Schema    string
	Table     string
	Column    string
	RefSchema string
	RefTable  string
	RefColumn string
}

// LiveSchema is the introspected shape of a database. It is dialect-tagged
// so the generated DDL — and the db::<dialect>:: nodes the SQL extractor
// derives from it — carry the right dialect.
type LiveSchema struct {
	Dialect     string
	Columns     []LiveColumn
	ForeignKeys []LiveForeignKey
}

// IntrospectPostgres connects to a PostgreSQL DSN and reads its table /
// column / foreign-key shape from information_schema. schema filters to a
// single schema (default "public" when empty); pass "*" for every
// non-system schema.
func IntrospectPostgres(ctx context.Context, dsn, schema string) (*LiveSchema, error) {
	if schema == "" {
		schema = "public"
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	ls := &LiveSchema{Dialect: "postgres"}
	schemaFilter := func(base string, col string) (string, []any) {
		if schema == "*" {
			return base + " AND " + col + " NOT IN ('pg_catalog','information_schema')", nil
		}
		return base + " AND " + col + " = $1", []any{schema}
	}

	// Columns.
	colSQL, colArgs := schemaFilter(
		`SELECT table_schema, table_name, column_name, data_type, is_nullable, ordinal_position
		 FROM information_schema.columns WHERE true`, "table_schema")
	colSQL += " ORDER BY table_schema, table_name, ordinal_position"
	rows, err := conn.Query(ctx, colSQL, colArgs...)
	if err != nil {
		return nil, fmt.Errorf("query columns: %w", err)
	}
	for rows.Next() {
		var c LiveColumn
		var nullable string
		if err := rows.Scan(&c.Schema, &c.Table, &c.Name, &c.DataType, &nullable, &c.Ordinal); err != nil {
			rows.Close()
			return nil, err
		}
		c.Nullable = strings.EqualFold(nullable, "YES")
		ls.Columns = append(ls.Columns, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Primary keys — mark the matching columns.
	pkSQL, pkArgs := schemaFilter(
		`SELECT tc.table_schema, tc.table_name, kcu.column_name
		 FROM information_schema.table_constraints tc
		 JOIN information_schema.key_column_usage kcu
		   ON tc.constraint_name = kcu.constraint_name AND tc.table_schema = kcu.table_schema
		 WHERE tc.constraint_type = 'PRIMARY KEY'`, "tc.table_schema")
	pkSet := map[string]bool{}
	if rows, err := conn.Query(ctx, pkSQL, pkArgs...); err == nil {
		for rows.Next() {
			var s, t, c string
			if err := rows.Scan(&s, &t, &c); err == nil {
				pkSet[s+"."+t+"."+c] = true
			}
		}
		rows.Close()
	}
	for i := range ls.Columns {
		c := &ls.Columns[i]
		if pkSet[c.Schema+"."+c.Table+"."+c.Name] {
			c.IsPrimaryKey = true
		}
	}

	// Foreign keys.
	fkSQL, fkArgs := schemaFilter(
		`SELECT tc.table_schema, tc.table_name, kcu.column_name,
		        ccu.table_schema, ccu.table_name, ccu.column_name
		 FROM information_schema.table_constraints tc
		 JOIN information_schema.key_column_usage kcu
		   ON tc.constraint_name = kcu.constraint_name AND tc.table_schema = kcu.table_schema
		 JOIN information_schema.constraint_column_usage ccu
		   ON tc.constraint_name = ccu.constraint_name AND tc.table_schema = ccu.table_schema
		 WHERE tc.constraint_type = 'FOREIGN KEY'`, "tc.table_schema")
	if rows, err := conn.Query(ctx, fkSQL, fkArgs...); err == nil {
		for rows.Next() {
			var fk LiveForeignKey
			if err := rows.Scan(&fk.Schema, &fk.Table, &fk.Column, &fk.RefSchema, &fk.RefTable, &fk.RefColumn); err == nil {
				ls.ForeignKeys = append(ls.ForeignKeys, fk)
			}
		}
		rows.Close()
	}
	return ls, nil
}

// ToDDL renders the introspected schema as standard CREATE TABLE +
// foreign-key DDL. Feeding this through Gortex's SQL extractor produces
// the same db::<dialect>:: table / column nodes (and reference edges) as
// indexing hand-written migrations — so a live database becomes graph
// nodes with no new ingestion path. The output is deterministic
// (tables sorted; columns in ordinal order).
func (ls *LiveSchema) ToDDL() string {
	type tableKey struct{ schema, table string }
	order := []tableKey{}
	byTable := map[tableKey][]LiveColumn{}
	for _, c := range ls.Columns {
		k := tableKey{c.Schema, c.Table}
		if _, ok := byTable[k]; !ok {
			order = append(order, k)
		}
		byTable[k] = append(byTable[k], c)
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].schema != order[j].schema {
			return order[i].schema < order[j].schema
		}
		return order[i].table < order[j].table
	})

	qualify := func(schema, table string) string {
		if schema == "" || schema == "public" {
			return table
		}
		return schema + "." + table
	}

	var b strings.Builder
	b.WriteString("-- Generated by `gortex db schema` from a live ")
	b.WriteString(ls.Dialect)
	b.WriteString(" database. Do not edit.\n\n")
	for _, k := range order {
		cols := byTable[k]
		sort.SliceStable(cols, func(i, j int) bool { return cols[i].Ordinal < cols[j].Ordinal })
		fmt.Fprintf(&b, "CREATE TABLE %s (\n", qualify(k.schema, k.table))
		var pks []string
		for i, c := range cols {
			line := "    " + c.Name + " " + sqlType(c.DataType)
			if !c.Nullable {
				line += " NOT NULL"
			}
			if i < len(cols)-1 || hasPK(cols) {
				line += ","
			}
			b.WriteString(line + "\n")
			if c.IsPrimaryKey {
				pks = append(pks, c.Name)
			}
		}
		if len(pks) > 0 {
			fmt.Fprintf(&b, "    PRIMARY KEY (%s)\n", strings.Join(pks, ", "))
		}
		b.WriteString(");\n\n")
	}
	for _, fk := range ls.ForeignKeys {
		fmt.Fprintf(&b, "ALTER TABLE %s ADD FOREIGN KEY (%s) REFERENCES %s (%s);\n",
			qualify(fk.Schema, fk.Table), fk.Column,
			qualify(fk.RefSchema, fk.RefTable), fk.RefColumn)
	}
	return b.String()
}

func hasPK(cols []LiveColumn) bool {
	for _, c := range cols {
		if c.IsPrimaryKey {
			return true
		}
	}
	return false
}

// sqlType normalises an information_schema data_type to a portable column
// type token the SQL extractor recognises; unknown types pass through.
func sqlType(t string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return "text"
	}
	return t
}
