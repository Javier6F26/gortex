package indexer

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// slowExtractor is a parser.Extractor whose Extract sleeps, used to
// exercise the per-file extraction timeout.
type slowExtractor struct{ delay time.Duration }

func (s *slowExtractor) Language() string     { return "slow" }
func (s *slowExtractor) Extensions() []string { return []string{".slow"} }
func (s *slowExtractor) Extract(filePath string, _ []byte) (*parser.ExtractionResult, error) {
	time.Sleep(s.delay)
	return &parser.ExtractionResult{
		Nodes: []*graph.Node{{ID: filePath, Kind: graph.KindFile, Name: filePath}},
	}, nil
}

func TestExtractWithTimeout_NoBudget(t *testing.T) {
	idx := newTestIndexer(graph.New()) // MaxExtractMillis = 0
	r, err := idx.extractWithTimeout(&slowExtractor{delay: 5 * time.Millisecond}, "x.slow", []byte("x"))
	require.NoError(t, err)
	require.NotNil(t, r)
}

func TestExtractWithTimeout_FastFileUnderBudget(t *testing.T) {
	idx := newTestIndexer(graph.New())
	idx.config.MaxExtractMillis = 2000
	r, err := idx.extractWithTimeout(&slowExtractor{delay: 5 * time.Millisecond}, "x.slow", []byte("x"))
	require.NoError(t, err)
	require.NotNil(t, r)
}

func TestExtractWithTimeout_SlowFileTimesOut(t *testing.T) {
	idx := newTestIndexer(graph.New())
	idx.config.MaxExtractMillis = 50
	_, err := idx.extractWithTimeout(&slowExtractor{delay: 800 * time.Millisecond}, "x.slow", []byte("x"))
	require.ErrorIs(t, err, errExtractTimeout)
}

func TestSizeSkipNode(t *testing.T) {
	n := sizeSkipNode(skippedFile{relPath: "gen/big.ts", lang: "typescript", size: 9_000_000}, 2_000_000)
	require.Equal(t, graph.KindFile, n.Kind)
	require.Equal(t, "gen/big.ts", n.ID)
	require.Equal(t, "big.ts", n.Name)
	require.Equal(t, "typescript", n.Language)
	require.Equal(t, true, n.Meta["skipped_due_to_size"])
	require.Equal(t, int64(9_000_000), n.Meta["file_size_bytes"])
}

func TestTimeoutSkipResult(t *testing.T) {
	r := timeoutSkipResult("huge.ts", "typescript", 10000)
	require.Len(t, r.Nodes, 1)
	require.Equal(t, true, r.Nodes[0].Meta["skipped_due_to_timeout"])
	require.Equal(t, 10000, r.Nodes[0].Meta["extract_budget_ms"])
}

// TestIndex_SizeSkipTelemetry verifies a file dropped by the size cap
// becomes a synthetic node carrying skip telemetry instead of vanishing.
func TestIndex_SizeSkipTelemetry(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "small.go"), "package main\n\nfunc A() {}\n")
	writeFile(t, filepath.Join(dir, "big.go"),
		"package main\n"+strings.Repeat("// filler line for bulk\n", 5000))

	g := graph.New()
	idx := newTestIndexer(g)
	idx.config.MaxFileSize = 1024 // big.go (~115 KB) is over the cap

	result, err := idx.Index(dir)
	require.NoError(t, err)
	require.Equal(t, 1, result.FileCount)   // only small.go parsed
	require.Equal(t, 1, result.SkippedFiles) // big.go skipped

	n := g.GetNode("big.go")
	require.NotNil(t, n, "size-skipped file must still have a node")
	require.Equal(t, graph.KindFile, n.Kind)
	require.Equal(t, true, n.Meta["skipped_due_to_size"])
	require.Equal(t, int64(1024), n.Meta["max_file_size_bytes"])
}

// TestIndex_ExtractTimeoutSkip verifies a file whose extraction blows
// the time budget is skipped with a synthetic timeout node and the
// index pass still completes.
func TestIndex_ExtractTimeoutSkip(t *testing.T) {
	reg := parser.NewRegistry()
	reg.Register(&slowExtractor{delay: 800 * time.Millisecond})
	cfg := config.Default().Index
	cfg.Workers = 1
	cfg.MaxExtractMillis = 80
	idx := New(graph.New(), reg, cfg, zap.NewNop())

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.slow"), "some content")

	result, err := idx.Index(dir)
	require.NoError(t, err)
	require.Equal(t, 1, result.SkippedFiles)

	n := idx.graph.GetNode("a.slow")
	require.NotNil(t, n)
	require.Equal(t, true, n.Meta["skipped_due_to_timeout"])
}

func TestIndexFile_SizeSkip(t *testing.T) {
	dir := t.TempDir()
	g := graph.New()
	idx := newTestIndexer(g)
	idx.config.MaxFileSize = 512
	if _, err := idx.Index(dir); err != nil {
		t.Fatalf("index: %v", err)
	}

	big := filepath.Join(dir, "huge.go")
	writeFile(t, big, "package main\n"+strings.Repeat("// x\n", 2000))
	require.NoError(t, idx.indexFile(big, false))

	n := g.GetNode("huge.go")
	require.NotNil(t, n)
	require.Equal(t, true, n.Meta["skipped_due_to_size"])
}

// TestSkipNodes_CarryUnifiedReason verifies every skip-node shape stamps a
// uniform skip_reason so index_health can roll them up by reason.
func TestSkipNodes_CarryUnifiedReason(t *testing.T) {
	cases := []struct {
		name   string
		node   *graph.Node
		reason string
	}{
		{"size", sizeSkipNode(skippedFile{relPath: "big.go", lang: "go", size: 1 << 20}, 1024), "size"},
		{"timeout", timeoutSkipResult("slow.go", "go", 500).Nodes[0], "timeout"},
		{"minified", minifiedSkipResult("bundle.js", "javascript", "long-lines").Nodes[0], "minified"},
		{"parse_failed", parseFailedSkipResult("bad.go", "go", errors.New("boom")).Nodes[0], "parse_failed"},
		{"parse_panic", quarantineResult("crash.go", "go", "panic").Nodes[0], "parse_panic"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, graph.KindFile, c.node.Kind)
			require.Equal(t, c.reason, c.node.Meta["skip_reason"])
		})
	}
}

func TestParseFailedSkipResult_RecordsError(t *testing.T) {
	r := parseFailedSkipResult("bad.go", "go", errors.New("unexpected token"))
	require.Len(t, r.Nodes, 1)
	n := r.Nodes[0]
	require.Equal(t, "bad.go", n.FilePath)
	require.Equal(t, "bad.go", n.Name)
	require.Equal(t, "parse_failed", n.Meta["skip_reason"])
	require.Equal(t, "unexpected token", n.Meta["parse_error"])
}

// TestIndex_ParseFailedSkipTelemetry verifies a file that fails to parse
// during a FULL index stays visible as a parse_failed skip node instead of
// vanishing — the safe counterpart to the live-modify path, which keeps a
// file's prior nodes through a transient failure (see
// TestPatchGraphModify_ParseFailureKeepsPriorNodes).
func TestIndex_ParseFailedSkipTelemetry(t *testing.T) {
	idx, ext := newToggleIndexer(t)
	ext.setFail(true) // every extraction returns an error

	dir := t.TempDir()
	idx.SetRootPath(dir)
	writeFile(t, filepath.Join(dir, "broken.fk"), "this does not parse")

	_, err := idx.Index(dir)
	require.NoError(t, err)

	n := idx.graph.GetNode("broken.fk")
	require.NotNil(t, n, "a full-index parse failure must leave a visible skip node")
	require.Equal(t, graph.KindFile, n.Kind)
	require.Equal(t, "parse_failed", n.Meta["skip_reason"])
}
