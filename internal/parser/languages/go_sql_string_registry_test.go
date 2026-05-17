package languages

import (
	"strings"
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

// Every SQL call also seeds a KindString context="sql" registry
// node and an EdgeEmits from the caller. This lets the sql_rebuild
// analyzer rederive the table layer from the string registry
// without re-parsing source.
func TestGoSQL_EmitsKindStringRegistryNode(t *testing.T) {
	src := `package foo

import _ "github.com/lib/pq"

func Run(db DB) {
	db.Query("SELECT id FROM users WHERE active = true")
}

type DB struct{}

func (DB) Query(q string, args ...any) {}
`
	fix := runGoExtract(t, src)

	// One KindString context="sql" node with dialect=postgres.
	var sqlStrings []*graph.Node
	for _, n := range fix.nodesByKind[graph.KindString] {
		if ctx, _ := n.Meta["context"].(string); ctx == "sql" {
			sqlStrings = append(sqlStrings, n)
		}
	}
	if len(sqlStrings) != 1 {
		t.Fatalf("expected 1 KindString context=sql node, got %d", len(sqlStrings))
	}
	n := sqlStrings[0]
	if got, _ := n.Meta["dialect"].(string); got != "postgres" {
		t.Errorf("dialect meta = %q, want postgres", got)
	}
	tables, _ := n.Meta["tables"].([]string)
	if len(tables) != 1 || !strings.Contains(tables[0], "users") {
		t.Errorf("tables meta = %v, want one entry containing users", tables)
	}

	// One EdgeEmits from the caller to that KindString.
	emits := 0
	for _, e := range fix.edgesByKind[graph.EdgeEmits] {
		if e.To != n.ID {
			continue
		}
		emits++
		if e.From != "pkg/foo.go::Run" {
			t.Errorf("emit From = %q, want pkg/foo.go::Run", e.From)
		}
		if ctx, _ := e.Meta["context"].(string); ctx != "sql" {
			t.Errorf("edge context meta = %q", ctx)
		}
		if dialect, _ := e.Meta["dialect"].(string); dialect != "postgres" {
			t.Errorf("edge dialect meta = %q", dialect)
		}
	}
	if emits != 1 {
		t.Errorf("expected 1 EdgeEmits to KindString, got %d", emits)
	}

	// EdgeQueries to KindTable still emits — short-circuit registry
	// runs ALONGSIDE the existing extraction path.
	queries := fix.edgesByKind[graph.EdgeQueries]
	if len(queries) != 1 {
		t.Errorf("expected 1 EdgeQueries, got %d", len(queries))
	}
}

func TestGoSQL_KindStringRegistryDedupedAcrossCallSites(t *testing.T) {
	// Two callers issuing the identical query share one KindString
	// node but produce two EdgeEmits, one per caller.
	src := `package foo

func A(db DB) { db.Exec("UPDATE accounts SET balance = 0") }
func B(db DB) { db.Exec("UPDATE accounts SET balance = 0") }

type DB struct{}

func (DB) Exec(q string, args ...any) {}
`
	fix := runGoExtract(t, src)
	var sqlStrings []*graph.Node
	for _, n := range fix.nodesByKind[graph.KindString] {
		if ctx, _ := n.Meta["context"].(string); ctx == "sql" {
			sqlStrings = append(sqlStrings, n)
		}
	}
	if len(sqlStrings) != 1 {
		t.Fatalf("expected 1 deduped KindString sql node, got %d", len(sqlStrings))
	}
	emits := 0
	for _, e := range fix.edgesByKind[graph.EdgeEmits] {
		if e.To == sqlStrings[0].ID {
			emits++
		}
	}
	if emits != 2 {
		t.Errorf("expected 2 EdgeEmits (one per caller), got %d", emits)
	}
}
