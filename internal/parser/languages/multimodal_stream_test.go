package languages

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

// TestPDFExtractorStream_FileNodeOnMalformed mirrors the byte-path behaviour:
// a malformed PDF read through the streaming route still yields exactly the
// KindFile node and never panics (the per-document recover holds).
func TestPDFExtractorStream_FileNodeOnMalformed(t *testing.T) {
	e := NewPDFExtractor()
	data := []byte("%PDF-1.4 not really a pdf")
	var nodes []*graph.Node
	require.NotPanics(t, func() {
		err := e.ExtractStream("spec.pdf", bytes.NewReader(data), int64(len(data)),
			func(n *graph.Node, _ []*graph.Edge) { nodes = append(nodes, n) })
		require.NoError(t, err)
	})
	require.Len(t, nodes, 1)
	require.Equal(t, graph.KindFile, nodes[0].Kind)
	require.Equal(t, "pdf", nodes[0].Meta["asset_kind"])
	require.Equal(t, len(data), nodes[0].Meta["size_bytes"])
}
