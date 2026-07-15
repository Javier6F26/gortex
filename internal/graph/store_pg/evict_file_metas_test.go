package store_pg_test

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/graph/store_pg"
)

// Evicting a repo must clear its rows from the `files` table too — leaving
// them behind poisons the mtime/hash census: an untracked repo's stale rows
// make a later fresh track (or reindex) skip files whose recorded hash
// matches disk, so their nodes are never re-minted and the index serves
// stale content forever. (Observed live: vault re-track after untrack kept
// serving an old constitution.)
func TestEvictRepo_CleansFileMetas(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema := createTestSchema(t)
	st := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schema})

	st.AddNode(&graph.Node{ID: "repoA/a.go::Foo", Kind: graph.KindFunction, FilePath: "repoA/a.go", RepoPrefix: "repoA"})
	st.AddNode(&graph.Node{ID: "repoB/b.go::Bar", Kind: graph.KindFunction, FilePath: "repoB/b.go", RepoPrefix: "repoB"})
	if err := st.SetFileMetas("repoA", []graph.FileMetaRow{
		{FilePath: "repoA/a.go", ContentHash: "hash_a", Size: 3, NodeCount: 1},
	}); err != nil {
		t.Fatalf("SetFileMetas A: %v", err)
	}
	if err := st.SetFileMetas("repoB", []graph.FileMetaRow{
		{FilePath: "repoB/b.go", ContentHash: "hash_b", Size: 3, NodeCount: 1},
	}); err != nil {
		t.Fatalf("SetFileMetas B: %v", err)
	}

	st.EvictRepo("repoA")

	if metas, err := st.FileMetasForRepo("repoA"); err != nil || len(metas) != 0 {
		t.Fatalf("evicted repoA still has %d file meta rows (err=%v) — census poisoning", len(metas), err)
	}
	if metas, err := st.FileMetasForRepo("repoB"); err != nil || len(metas) != 1 {
		t.Fatalf("repoB file metas must survive repoA eviction, got %d (err=%v)", len(metas), err)
	}
}

// Evicting a single file must drop its `files` row for the same census
// reason: the per-file path is used by the lone-repo migration and the
// unprefixed-repo untrack branch.
func TestEvictFile_CleansFileMetas(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema := createTestSchema(t)
	st := openHardenStore(t, store_pg.Config{DSN: dsn, Schema: schema})

	st.AddNode(&graph.Node{ID: "repoA/a.go::Foo", Kind: graph.KindFunction, FilePath: "repoA/a.go", RepoPrefix: "repoA"})
	st.AddNode(&graph.Node{ID: "repoA/b.go::Baz", Kind: graph.KindFunction, FilePath: "repoA/b.go", RepoPrefix: "repoA"})
	if err := st.SetFileMetas("repoA", []graph.FileMetaRow{
		{FilePath: "repoA/a.go", ContentHash: "hash_a", Size: 3, NodeCount: 1},
		{FilePath: "repoA/b.go", ContentHash: "hash_b", Size: 3, NodeCount: 1},
	}); err != nil {
		t.Fatalf("SetFileMetas: %v", err)
	}

	st.EvictFile("repoA/a.go")

	metas, err := st.FileMetasForRepo("repoA")
	if err != nil {
		t.Fatalf("FileMetasForRepo: %v", err)
	}
	if len(metas) != 1 || metas[0].FilePath != "repoA/b.go" {
		t.Fatalf("EvictFile must remove exactly repoA/a.go's meta row, got %+v", metas)
	}
}
