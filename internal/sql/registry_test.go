package sql

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func TestRebuildTablesFromStringRegistry_BuildsTablesAndEdges(t *testing.T) {
	g := graph.New()
	// Caller (any node kind will do — the rebuild reads emitters
	// from EdgeEmits.From regardless).
	g.AddNode(&graph.Node{ID: "pkg/foo.go::Run", Kind: graph.KindFunction, Name: "Run"})
	// KindString context="sql" registry node.
	strID := "string::sql::SELECT id FROM users"
	g.AddNode(&graph.Node{
		ID:       strID,
		Kind:     graph.KindString,
		Name:     "SELECT id FROM users",
		Language: "go",
		Meta: map[string]any{
			"context": "sql",
			"value":   "SELECT id FROM users",
			"dialect": "postgres",
		},
	})
	g.AddEdge(&graph.Edge{
		From: "pkg/foo.go::Run",
		To:   strID,
		Kind: graph.EdgeEmits,
		Meta: map[string]any{"context": "sql", "dialect": "postgres"},
	})

	stats := RebuildTablesFromStringRegistry(g)

	if stats.StringsVisited != 1 {
		t.Errorf("StringsVisited = %d, want 1", stats.StringsVisited)
	}
	if stats.TablesCreated != 1 {
		t.Errorf("TablesCreated = %d, want 1", stats.TablesCreated)
	}
	if stats.QueryEdges != 1 {
		t.Errorf("QueryEdges = %d, want 1", stats.QueryEdges)
	}
	if stats.EmittersLinked != 1 {
		t.Errorf("EmittersLinked = %d, want 1", stats.EmittersLinked)
	}

	tableID := TableNodeID("postgres", "", "users")
	if n := g.GetNode(tableID); n == nil {
		t.Fatalf("expected KindTable %q to be created", tableID)
	} else if n.Kind != graph.KindTable {
		t.Errorf("node kind = %s, want %s", n.Kind, graph.KindTable)
	}
	// EdgeQueries from caller to table.
	in := g.GetInEdges(tableID)
	hasQueries := false
	for _, e := range in {
		if e.Kind == graph.EdgeQueries && e.From == "pkg/foo.go::Run" {
			hasQueries = true
			if e.Origin != graph.OriginTextMatched {
				t.Errorf("Origin = %q, want text_matched", e.Origin)
			}
		}
	}
	if !hasQueries {
		t.Errorf("missing EdgeQueries from caller to %s", tableID)
	}
}

func TestRebuildTablesFromStringRegistry_Idempotent(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{ID: "pkg/foo.go::Run", Kind: graph.KindFunction, Name: "Run"})
	g.AddNode(&graph.Node{
		ID:   "string::sql::UPDATE users SET active = true",
		Kind: graph.KindString,
		Name: "UPDATE users SET active = true",
		Meta: map[string]any{"context": "sql", "dialect": "postgres"},
	})
	g.AddEdge(&graph.Edge{
		From: "pkg/foo.go::Run",
		To:   "string::sql::UPDATE users SET active = true",
		Kind: graph.EdgeEmits,
	})

	first := RebuildTablesFromStringRegistry(g)
	if first.TablesCreated == 0 {
		t.Fatalf("first pass created no tables: %+v", first)
	}
	second := RebuildTablesFromStringRegistry(g)
	if second.TablesCreated != 0 {
		t.Errorf("second pass created %d tables, expected idempotent", second.TablesCreated)
	}
	if second.QueryEdges != 0 {
		t.Errorf("second pass added %d query edges, expected idempotent", second.QueryEdges)
	}
}

func TestRebuildTablesFromStringRegistry_SkipsNonSQLStrings(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{ID: "pkg/foo.go::Run", Kind: graph.KindFunction, Name: "Run"})
	// error_msg KindString — should be ignored.
	g.AddNode(&graph.Node{
		ID:   "string::error_msg::bad token",
		Kind: graph.KindString,
		Name: "bad token",
		Meta: map[string]any{"context": "error_msg"},
	})
	g.AddEdge(&graph.Edge{
		From: "pkg/foo.go::Run",
		To:   "string::error_msg::bad token",
		Kind: graph.EdgeEmits,
	})

	stats := RebuildTablesFromStringRegistry(g)
	if stats.StringsVisited != 0 {
		t.Errorf("StringsVisited = %d, want 0 (no sql-context strings)", stats.StringsVisited)
	}
	if stats.TablesCreated != 0 {
		t.Errorf("TablesCreated = %d, want 0", stats.TablesCreated)
	}
}

func TestRebuildTablesFromStringRegistry_SkipsUnparseableQueries(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{ID: "pkg/foo.go::Run", Kind: graph.KindFunction, Name: "Run"})
	g.AddNode(&graph.Node{
		ID:   "string::sql::not actually sql",
		Kind: graph.KindString,
		Name: "not actually sql",
		Meta: map[string]any{"context": "sql"},
	})

	stats := RebuildTablesFromStringRegistry(g)
	if stats.StringsVisited != 1 {
		t.Errorf("StringsVisited = %d, want 1", stats.StringsVisited)
	}
	if stats.TablesCreated != 0 {
		t.Errorf("TablesCreated = %d, want 0", stats.TablesCreated)
	}
	if stats.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", stats.Skipped)
	}
}

func TestRebuildTablesFromStringRegistry_EmitsColumnEdges(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{ID: "pkg/foo.go::Run", Kind: graph.KindFunction, Name: "Run"})
	g.AddNode(&graph.Node{
		ID:   "string::sql::INSERT INTO accounts (id, balance) VALUES (1, 10)",
		Kind: graph.KindString,
		Name: "INSERT INTO accounts (id, balance) VALUES (1, 10)",
		Meta: map[string]any{"context": "sql", "dialect": "postgres"},
	})
	g.AddEdge(&graph.Edge{
		From: "pkg/foo.go::Run",
		To:   "string::sql::INSERT INTO accounts (id, balance) VALUES (1, 10)",
		Kind: graph.EdgeEmits,
	})

	stats := RebuildTablesFromStringRegistry(g)
	if stats.ColumnsCreated != 2 {
		t.Errorf("ColumnsCreated = %d, want 2 (id, balance)", stats.ColumnsCreated)
	}
	if stats.WriteColEdges != 2 {
		t.Errorf("WriteColEdges = %d, want 2 (INSERT is a write)", stats.WriteColEdges)
	}
}

func TestRebuildTablesFromStringRegistry_NilGraphSafe(t *testing.T) {
	stats := RebuildTablesFromStringRegistry(nil)
	if stats.StringsVisited != 0 {
		t.Errorf("nil graph produced StringsVisited = %d, want 0", stats.StringsVisited)
	}
}
