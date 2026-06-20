package indexer

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// streamProbeExtractor tags the file node differently on its byte Extract vs
// its streaming ExtractStream route, so a test can prove which path the
// indexer took.
type streamProbeExtractor struct{}

func (streamProbeExtractor) Language() string     { return "streamprobe" }
func (streamProbeExtractor) Extensions() []string { return []string{".streamprobe"} }

func (streamProbeExtractor) Extract(filePath string, _ []byte) (*parser.ExtractionResult, error) {
	return &parser.ExtractionResult{Nodes: []*graph.Node{{
		ID: filePath, Kind: graph.KindFile, FilePath: filePath,
		Meta: map[string]any{"via": "bytes"},
	}}}, nil
}

func (streamProbeExtractor) ExtractStream(filePath string, _ io.ReaderAt, size int64, emit func(*graph.Node, []*graph.Edge)) error {
	emit(&graph.Node{
		ID: filePath, Kind: graph.KindFile, FilePath: filePath,
		Meta: map[string]any{"via": "stream", "size_bytes": int(size)},
	}, nil)
	return nil
}

// TestBulkIndexPrefersStreamingExtractor verifies the bulk worker takes the
// StreamingExtractor route (file read by handle, one unit at a time) rather
// than the byte Extract path when an extractor offers both.
func TestBulkIndexPrefersStreamingExtractor(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "doc.streamprobe"), "irrelevant content body")

	g := graph.New()
	reg := parser.NewRegistry()
	reg.Register(streamProbeExtractor{})
	idx := New(g, reg, config.Default().Index, zap.NewNop())
	_, err := idx.Index(dir)
	require.NoError(t, err)

	var fileNode *graph.Node
	for _, n := range g.AllNodes() {
		if n.Kind == graph.KindFile && filepath.Base(n.FilePath) == "doc.streamprobe" {
			fileNode = n
			break
		}
	}
	require.NotNil(t, fileNode, "the .streamprobe file must be indexed")
	require.Equal(t, "stream", fileNode.Meta["via"],
		"indexer must take the StreamingExtractor route, not the byte Extract path")
}

// TestExtractStreamingHelper checks the helper opens the file by handle and
// passes the on-disk size through from Stat.
func TestExtractStreamingHelper(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.streamprobe")
	require.NoError(t, os.WriteFile(p, []byte("hello"), 0o644))

	idx := New(graph.New(), parser.NewRegistry(), config.Default().Index, zap.NewNop())
	res, err := idx.extractStreaming(streamProbeExtractor{}, p, "x.streamprobe")
	require.NoError(t, err)
	require.Len(t, res.Nodes, 1)
	require.Equal(t, "stream", res.Nodes[0].Meta["via"])
	require.Equal(t, 5, res.Nodes[0].Meta["size_bytes"], "size passed from the file handle's Stat")
}
