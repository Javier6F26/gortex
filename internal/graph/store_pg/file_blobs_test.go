package store_pg_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/graph/store_pg"
)

// A blob is deduplicated on content hash: two repos with a byte-identical
// file share one file_blobs row, and the bytes round-trip exactly.
func TestFileBlobs_DedupAndByteExact(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema := createTestSchema(t)
	st := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schema})

	body := []byte("package main\n\nfunc Foo() int { return 42 }\n")
	hash := "hash_shared_1"

	// Two files (different repos) referencing the same content hash.
	if err := st.SetFileMetas("repoA", []graph.FileMetaRow{
		{FilePath: "repoA/a.go", ContentHash: hash, Size: len(body), NodeCount: 1},
	}); err != nil {
		t.Fatalf("SetFileMetas A: %v", err)
	}
	if err := st.SetFileMetas("repoB", []graph.FileMetaRow{
		{FilePath: "repoB/b.go", ContentHash: hash, Size: len(body), NodeCount: 1},
	}); err != nil {
		t.Fatalf("SetFileMetas B: %v", err)
	}
	// PutFileBlobs twice with the same hash — dedups.
	for i := 0; i < 2; i++ {
		if err := st.PutFileBlobs([]graph.FileBlob{{ContentHash: hash, Body: body, Size: len(body)}}); err != nil {
			t.Fatalf("PutFileBlobs: %v", err)
		}
	}

	// Byte-exact retrieval by both paths and by hash.
	for _, tc := range []struct{ repo, path string }{{"repoA", "repoA/a.go"}, {"repoB", "repoB/b.go"}} {
		blob, ok := st.GetFileBlobByPath(tc.repo, tc.path)
		if !ok {
			t.Fatalf("GetFileBlobByPath(%s): not found", tc.path)
		}
		if string(blob.Body) != string(body) {
			t.Errorf("blob body mismatch for %s: got %q", tc.path, blob.Body)
		}
		if blob.ContentHash != hash {
			t.Errorf("blob hash = %q, want %q", blob.ContentHash, hash)
		}
	}
	if blob, ok := st.GetFileBlobByHash(hash); !ok || string(blob.Body) != string(body) {
		t.Errorf("GetFileBlobByHash mismatch: ok=%v body=%q", ok, blob.Body)
	}

	// Exactly one row for the shared hash. Schema-qualify the table — the
	// shared testRootPool's search_path is not pinned to this test's schema,
	// so an unqualified name can resolve against another test's schema (or
	// not at all) depending on run order.
	var count int
	if err := testRootPool.QueryRow(context.Background(),
		fmt.Sprintf(`SELECT count(*) FROM %s.file_blobs WHERE content_hash=$1`, schema), hash).Scan(&count); err != nil {
		t.Fatalf("count file_blobs: %v", err)
	}
	if count != 1 {
		t.Errorf("file_blobs rows for shared hash = %d, want 1 (dedup)", count)
	}
}

// GC removes blobs no longer referenced by any files row.
func TestFileBlobs_GCPrunesOrphans(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema := createTestSchema(t)
	st := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schema})

	// A referenced blob and an orphan blob.
	_ = st.SetFileMetas("repoA", []graph.FileMetaRow{
		{FilePath: "repoA/a.go", ContentHash: "live", Size: 3, NodeCount: 1},
	})
	if err := st.PutFileBlobs([]graph.FileBlob{
		{ContentHash: "live", Body: []byte("abc"), Size: 3},
		{ContentHash: "orphan", Body: []byte("xyz"), Size: 3},
	}); err != nil {
		t.Fatalf("PutFileBlobs: %v", err)
	}

	removed, err := st.GCFileBlobs()
	if err != nil {
		t.Fatalf("GCFileBlobs: %v", err)
	}
	if removed != 1 {
		t.Errorf("GC removed %d, want 1 (the orphan)", removed)
	}
	if _, ok := st.GetFileBlobByHash("live"); !ok {
		t.Error("referenced blob was wrongly GC'd")
	}
	if _, ok := st.GetFileBlobByHash("orphan"); ok {
		t.Error("orphan blob survived GC")
	}
}

// A read-only store refuses blob writes.
func TestFileBlobs_ReadOnlyRefuses(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema := createTestSchema(t)
	wr := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schema})
	_ = wr // migrate schema

	ro := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schema, ReadOnly: true})
	if err := ro.PutFileBlobs([]graph.FileBlob{{ContentHash: "x", Body: []byte("y"), Size: 1}}); err != store_pg.ErrReadOnlyStore {
		t.Errorf("read-only PutFileBlobs = %v, want ErrReadOnlyStore", err)
	}
	if _, err := ro.GCFileBlobs(); err != store_pg.ErrReadOnlyStore {
		t.Errorf("read-only GCFileBlobs = %v, want ErrReadOnlyStore", err)
	}
}

// IndexedFileBlobs enumerates only files that have a stored blob, so a
// follower builds its diskless search over exactly the servable set.
func TestIndexedFileBlobs_OnlyBlobbedFiles(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema := createTestSchema(t)
	st := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schema})

	// Two files with blobs, one file (no blob — pre-blob or excluded).
	if err := st.SetFileMetas("repoA", []graph.FileMetaRow{
		{FilePath: "repoA/a.go", ContentHash: "ha", Size: 3, NodeCount: 1},
		{FilePath: "repoA/b.go", ContentHash: "hb", Size: 3, NodeCount: 1},
		{FilePath: "repoA/c.go", ContentHash: "hc", Size: 3, NodeCount: 1},
	}); err != nil {
		t.Fatalf("SetFileMetas: %v", err)
	}
	if err := st.PutFileBlobs([]graph.FileBlob{
		{ContentHash: "ha", Body: []byte("aaa"), Size: 3},
		{ContentHash: "hb", Body: []byte("bbb"), Size: 3},
	}); err != nil {
		t.Fatalf("PutFileBlobs: %v", err)
	}

	refs, err := st.IndexedFileBlobs()
	if err != nil {
		t.Fatalf("IndexedFileBlobs: %v", err)
	}
	got := map[string]string{}
	for _, r := range refs {
		got[r.FilePath] = r.ContentHash
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 blobbed files, got %d: %+v", len(got), got)
	}
	if got["repoA/a.go"] != "ha" || got["repoA/b.go"] != "hb" {
		t.Errorf("wrong hashes: %+v", got)
	}
	if _, ok := got["repoA/c.go"]; ok {
		t.Error("file with no blob must not be enumerated")
	}
}

// ContentByFile returns bodies in ordinal order for a content-class file.
func TestContentByFile_OrdinalOrder(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema := createTestSchema(t)
	st := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schema})
	if err := st.BuildContentIndex(); err != nil {
		t.Fatalf("BuildContentIndex: %v", err)
	}
	// Insert out of order; expect ordinal-sorted read-back.
	if err := st.AppendContent("repoA", []graph.ContentFTSItem{
		{NodeID: "repoA/doc.pdf#2", FilePath: "repoA/doc.pdf", Ordinal: 2, Body: "second"},
		{NodeID: "repoA/doc.pdf#0", FilePath: "repoA/doc.pdf", Ordinal: 0, Body: "zeroth"},
		{NodeID: "repoA/doc.pdf#1", FilePath: "repoA/doc.pdf", Ordinal: 1, Body: "first"},
	}); err != nil {
		t.Fatalf("AppendContent: %v", err)
	}
	items, err := st.ContentByFile("repoA", "repoA/doc.pdf")
	if err != nil {
		t.Fatalf("ContentByFile: %v", err)
	}
	got := make([]string, len(items))
	for i, it := range items {
		got[i] = it.Body
	}
	want := []string{"zeroth", "first", "second"}
	if len(got) != len(want) {
		t.Fatalf("ContentByFile returned %d items, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ordinal %d body = %q, want %q", i, got[i], want[i])
		}
	}
}
