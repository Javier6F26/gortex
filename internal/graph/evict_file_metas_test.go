package graph

import "testing"

// Evictions must clear in-memory census rows for parity with the disk
// stores — stale rows poison the mtime/hash census on re-track.
func TestEvict_CleansFileMetas(t *testing.T) {
	g := New()
	g.AddNode(&Node{ID: "repoA/a.go::Foo", Kind: KindFunction, FilePath: "repoA/a.go", RepoPrefix: "repoA"})
	g.AddNode(&Node{ID: "repoA/b.go::Bar", Kind: KindFunction, FilePath: "repoA/b.go", RepoPrefix: "repoA"})
	if err := g.SetFileMetas("repoA", []FileMetaRow{
		{FilePath: "repoA/a.go", ContentHash: "ha", Size: 1, NodeCount: 1},
		{FilePath: "repoA/b.go", ContentHash: "hb", Size: 1, NodeCount: 1},
	}); err != nil {
		t.Fatal(err)
	}

	g.EvictFile("repoA/a.go")
	metas, _ := g.FileMetasForRepo("repoA")
	if len(metas) != 1 || metas[0].FilePath != "repoA/b.go" {
		t.Fatalf("EvictFile must drop exactly repoA/a.go's census row, got %+v", metas)
	}

	g.EvictRepo("repoA")
	metas, _ = g.FileMetasForRepo("repoA")
	if len(metas) != 0 {
		t.Fatalf("EvictRepo must drop every census row, got %+v", metas)
	}
}
