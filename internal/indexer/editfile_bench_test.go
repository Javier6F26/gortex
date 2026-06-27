package indexer

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph/store_sqlite"
	"github.com/zzet/gortex/internal/parser"
	"github.com/zzet/gortex/internal/parser/languages"
)

// BenchmarkEditFileReindex measures the synchronous work an edit_file
// request pays after the text splice: reindexFile -> IndexFile ->
// indexFile(resolve=true). Because indexFile does NO change-diffing,
// re-indexing an unchanged file does the exact same work as a real edit,
// so we can measure the true hot path without writing to the repo.
//
// Corpus defaults to the repo root (../.. from internal/indexer) so the
// graph matches production scale; override with GORTEX_BENCH_CORPUS (a
// smaller subtree, e.g. internal/graph, runs far faster for before/after
// comparison since the FTS-delete cost scales with corpus size). Target
// file defaults to the sqlite store.go; override with GORTEX_BENCH_TARGET
// (path relative to the corpus root).
//
// Run (bypass rtk — it eats benchmark output):
//
//	rtk proxy go test ./internal/indexer -run '^$' -bench BenchmarkEditFileReindex \
//	    -benchtime=20x -cpuprofile=/tmp/edit.cpu
//	go tool pprof -top -nodecount=60 -cum /tmp/edit.cpu
func BenchmarkEditFileReindex(b *testing.B) {
	corpus := os.Getenv("GORTEX_BENCH_CORPUS")
	if corpus == "" {
		corpus = "../.." // repo root, relative to internal/indexer
	}
	corpus, err := filepath.Abs(corpus)
	if err != nil {
		b.Fatalf("abs corpus: %v", err)
	}
	if _, err := os.Stat(corpus); err != nil {
		b.Skipf("corpus %s not available: %v", corpus, err)
	}

	targetRel := os.Getenv("GORTEX_BENCH_TARGET")
	if targetRel == "" {
		targetRel = "internal/graph/store_sqlite/store.go"
	}
	target := filepath.Join(corpus, targetRel)
	if _, err := os.Stat(target); err != nil {
		b.Skipf("target %s not available: %v", target, err)
	}

	store, err := store_sqlite.Open(filepath.Join(b.TempDir(), "bench.sqlite"))
	if err != nil {
		b.Fatalf("open sqlite: %v", err)
	}
	defer func() { _ = store.Close() }()

	reg := parser.NewRegistry()
	languages.RegisterAll(reg)
	cfg := config.Default().Index
	cfg.Workers = runtime.NumCPU()

	idx := New(store, reg, cfg, zap.NewNop())

	// One-time bulk index (NOT timed). This is what the daemon already
	// did; we are measuring the per-edit cost on top of a warm graph.
	if _, err := idx.Index(corpus); err != nil {
		b.Fatalf("bulk index: %v", err)
	}
	st := store.Stats()
	b.Logf("graph: %d nodes, %d edges (corpus=%s, target=%s)",
		st.TotalNodes, st.TotalEdges, corpus, targetRel)

	// graphPath is the key indexFile derives for the reverse/resolver
	// passes — relKey + repo prefix, exactly as indexFile computes it.
	abs, _ := filepath.Abs(target)
	graphPath := idx.prefixPath(idx.relKey(abs))

	// 1. Full hot path: parse + evict + add + persist + resolve. This is
	//    what edit_file pays today.
	b.Run("IndexFile_resolve", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := idx.IndexFile(target); err != nil {
				b.Fatalf("IndexFile: %v", err)
			}
		}
	})

	// 2. Same minus the per-file resolver call — isolates the parse +
	//    EvictFile + AddBatch + persist (sidecar) cost, incl. the FTS
	//    upsert loop.
	b.Run("IndexFile_noresolve", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := idx.IndexFileNoResolve(target); err != nil {
				b.Fatalf("IndexFileNoResolve: %v", err)
			}
		}
	})

	// 3. The resolver pass alone — buildPassIndexes (4 whole-graph index
	//    builds) + forward + reverse resolve. (1)-(2) ~= this.
	b.Run("ResolveFileAndIncoming", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			idx.resolver.ResolveFileAndIncoming(graphPath)
		}
	})
}
