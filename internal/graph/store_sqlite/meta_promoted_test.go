package store_sqlite

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

// TestPromotedColumns_RoundTrip verifies the promoted keys land in their
// columns, are stripped from the JSON blob, and restore into Meta with
// exact types — while non-promoted keys stay in the blob.
func TestPromotedColumns_RoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "p.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	s.AddNode(&graph.Node{
		ID: "f.go::F", Kind: graph.KindFunction, Name: "F", FilePath: "f.go",
		Meta: map[string]any{
			"signature":  "func F()",
			"visibility": "public",
			"doc":        "F docs",
			"external":   true,
			"complexity": 5, // non-promoted — must stay in the blob
		},
	})

	n := s.GetNode("f.go::F")
	if n == nil {
		t.Fatal("GetNode returned nil")
	}
	assertType[string](t, n.Meta, "signature", "func F()")
	assertType[string](t, n.Meta, "visibility", "public")
	assertType[string](t, n.Meta, "doc", "F docs")
	assertType[bool](t, n.Meta, "external", true)
	assertType[int](t, n.Meta, "complexity", 5)

	var sig, vis, doc sql.NullString
	var ext sql.NullBool
	var blob []byte
	row := s.db.QueryRow(`SELECT signature, visibility, doc, external, meta FROM nodes WHERE id=?`, "f.go::F")
	if err := row.Scan(&sig, &vis, &doc, &ext, &blob); err != nil {
		t.Fatal(err)
	}
	if !sig.Valid || sig.String != "func F()" {
		t.Errorf("signature column = %+v", sig)
	}
	if !ext.Valid || !ext.Bool {
		t.Errorf("external column = %+v", ext)
	}
	blobStr := string(blob)
	for _, k := range []string{"signature", "visibility", "external"} {
		if strings.Contains(blobStr, k) {
			t.Errorf("blob still contains promoted key %q: %s", k, blobStr)
		}
	}
	if !strings.Contains(blobStr, "complexity") {
		t.Errorf("blob missing non-promoted key complexity: %s", blobStr)
	}
}

// TestPromotedColumns_ExternalFalse guards the NULL-vs-false distinction:
// a stored false must round-trip as false, not vanish.
func TestPromotedColumns_ExternalFalse(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "p.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	s.AddNode(&graph.Node{
		ID: "x", Kind: graph.KindFunction, Name: "x", FilePath: "x.go",
		Meta: map[string]any{"external": false},
	})
	n := s.GetNode("x")
	if n == nil {
		t.Fatal("nil")
	}
	v, ok := n.Meta["external"].(bool)
	if !ok || v != false {
		t.Errorf("external false: got %v (%T)", n.Meta["external"], n.Meta["external"])
	}
}

// TestPromotedColumns_Migration verifies ensureNodeColumns adds the
// promoted columns to a database created with the pre-promotion schema.
func TestPromotedColumns_Migration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.sqlite")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`CREATE TABLE nodes (
		id TEXT PRIMARY KEY, kind TEXT NOT NULL, name TEXT NOT NULL,
		qual_name TEXT NOT NULL DEFAULT '', file_path TEXT NOT NULL,
		start_line INTEGER NOT NULL DEFAULT 0, end_line INTEGER NOT NULL DEFAULT 0,
		language TEXT NOT NULL DEFAULT '', repo_prefix TEXT NOT NULL DEFAULT '',
		workspace_id TEXT NOT NULL DEFAULT '', project_id TEXT NOT NULL DEFAULT '',
		meta BLOB
	) WITHOUT ROWID`)
	if err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open old-schema db: %v", err)
	}
	defer func() { _ = s.Close() }()
	s.AddNode(&graph.Node{
		ID: "m", Kind: graph.KindFunction, Name: "m", FilePath: "m.go",
		Meta: map[string]any{"signature": "sig", "external": true},
	})
	n := s.GetNode("m")
	if n == nil {
		t.Fatal("nil after migration")
	}
	assertType[string](t, n.Meta, "signature", "sig")
	assertType[bool](t, n.Meta, "external", true)
}
