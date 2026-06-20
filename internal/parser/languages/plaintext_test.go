package languages

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTextExtractor_Sections(t *testing.T) {
	res, err := NewTextExtractor().Extract("notes.txt", []byte("first line\nsecond line\n"))
	require.NoError(t, err)

	file, secs := splitNodes(res.Nodes)
	require.NotNil(t, file)
	require.Equal(t, "text", file.Meta["asset_kind"])
	require.Len(t, secs, 1)
	require.Equal(t, "section", secs[0].Meta["asset_kind"])
	require.Equal(t, "first line\nsecond line", secs[0].Meta["section_text"])
	require.Equal(t, 1, secs[0].StartLine)
}

func TestTextExtractor_ChunksLargeFile(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 2000; i++ { // ~26 bytes * 2000 well over contentSectionCap
		sb.WriteString("the quick brown fox jumps\n")
	}
	res, err := NewTextExtractor().Extract("big.txt", []byte(sb.String()))
	require.NoError(t, err)

	_, secs := splitNodes(res.Nodes)
	require.Greater(t, len(secs), 1, "a large text file must split into multiple section chunks")
	for _, n := range secs {
		txt, _ := n.Meta["section_text"].(string)
		require.LessOrEqual(t, len(txt), contentSectionCap)
	}
}
