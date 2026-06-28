package indexer

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph/store_sqlite"
	"github.com/zzet/gortex/internal/parser"
	"github.com/zzet/gortex/internal/parser/languages"
	"go.uber.org/zap"
)

// TestMeasureEditLatency builds a sqlite-backed graph from a real repo and
// times ResolveFileAndIncoming + IndexFile (the per-edit reindex path), with
// the resolver's buildPassIndexes breakdown logged at Debug. It quantifies the
// whole-graph O(graph) cost a single-file edit pays. Opt-in:
//
//	GORTEX_MEASURE_REPO=/abs/path [GORTEX_MEASURE_FILE=rel/path.go] \
//	  go test -run TestMeasureEditLatency -v -count=1 ./internal/indexer/
func TestMeasureEditLatency(t *testing.T) {
	repo := os.Getenv("GORTEX_MEASURE_REPO")
	if repo == "" {
		t.Skip("set GORTEX_MEASURE_REPO=/abs/path to run")
	}
	editRel := os.Getenv("GORTEX_MEASURE_FILE")
	if editRel == "" {
		editRel = "internal/resolver/resolver.go"
	}

	dbPath := os.Getenv("GORTEX_MEASURE_DB")
	if dbPath == "" {
		dbPath = filepath.Join(t.TempDir(), "store.sqlite")
	}
	store, err := store_sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prebuilt := store.NodeCount() > 0 // reuse a persisted store across runs

	logger, _ := zap.NewDevelopment() // Debug enabled -> buildPassIndexes timing
	reg := parser.NewRegistry()
	reg.Register(languages.NewGoExtractor())
	reg.Register(languages.NewTypeScriptExtractor())
	reg.Register(languages.NewPythonExtractor())
	cfg := config.Default().Index
	cfg.Workers = 8
	idx := New(store, reg, cfg, logger)
	idx.resolver.SetLogger(logger) // surface buildPassIndexes per-builder timing

	if !prebuilt {
		t0 := time.Now()
		if _, err := idx.IndexCtx(testCtx(), repo); err != nil {
			t.Fatalf("index: %v", err)
		}
		t.Logf("=== indexed %s in %v -- nodes=%d edges=%d", repo, time.Since(t0), store.NodeCount(), store.EdgeCount())
	} else {
		t.Logf("=== reusing prebuilt store %s -- nodes=%d edges=%d", dbPath, store.NodeCount(), store.EdgeCount())
	}

	absEdit := filepath.Join(repo, editRel)
	if _, err := os.Stat(absEdit); err != nil {
		t.Fatalf("edit file not found: %v", err)
	}
	graphPath := idx.prefixPath(idx.relKey(absEdit))
	t.Logf("=== edit target graphPath=%q", graphPath)

	// The full edit_file reindex path end-to-end. #0 runs on the freshly
	// opened store (cold sqlite cache) = the cold-edit number; #1+ warm.
	for i := 0; i < 4; i++ {
		tt := time.Now()
		if err := idx.IndexFile(absEdit); err != nil {
			t.Logf("IndexFile err: %v", err)
		}
		t.Logf(">>> IndexFile #%d: %v", i, time.Since(tt))
	}
}
