package languages

import (
	"archive/zip"
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

func buildZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func splitNodes(nodes []*graph.Node) (file *graph.Node, docs []*graph.Node) {
	for _, n := range nodes {
		switch n.Kind {
		case graph.KindFile:
			file = n
		case graph.KindDoc:
			docs = append(docs, n)
		}
	}
	return file, docs
}

func TestPptxExtractor_Slides(t *testing.T) {
	data := buildZip(t, map[string]string{
		"ppt/slides/slide1.xml":           `<sld xmlns:a="urn:a"><a:t>Hello</a:t><a:t>World</a:t></sld>`,
		"ppt/slides/slide2.xml":           `<sld xmlns:a="urn:a"><a:t>Second deck slide</a:t></sld>`,
		"ppt/notesSlides/notesSlide1.xml": `<notes xmlns:a="urn:a"><a:t>ignored note</a:t></notes>`,
	})
	res, err := NewPptxExtractor().Extract("deck.pptx", data)
	require.NoError(t, err)

	file, slides := splitNodes(res.Nodes)
	require.NotNil(t, file)
	require.Equal(t, "pptx", file.Meta["asset_kind"])
	require.Equal(t, 2, file.Meta["slides"])
	require.Len(t, slides, 2, "notesSlides must not be indexed as slides")
	require.Equal(t, "slide", slides[0].Meta["asset_kind"])
	require.Equal(t, 1, slides[0].Meta["ordinal"])
	require.Equal(t, "Hello World", slides[0].Meta["section_text"])
	require.Equal(t, "Second deck slide", slides[1].Meta["section_text"])
	require.Len(t, res.Edges, 2)
}

func TestPptxExtractor_StreamMatchesBytes(t *testing.T) {
	data := buildZip(t, map[string]string{
		"ppt/slides/slide1.xml": `<sld xmlns:a="urn:a"><a:t>streamed text</a:t></sld>`,
	})
	var streamed []*graph.Node
	err := NewPptxExtractor().ExtractStream("deck.pptx", bytes.NewReader(data), int64(len(data)),
		func(n *graph.Node, _ []*graph.Edge) { streamed = append(streamed, n) })
	require.NoError(t, err)
	_, docs := splitNodes(streamed)
	require.Len(t, docs, 1)
	require.Equal(t, "streamed text", docs[0].Meta["section_text"])
}

func TestXlsxExtractor_Sheets(t *testing.T) {
	data := buildZip(t, map[string]string{
		"xl/sharedStrings.xml": `<sst><si><t>Revenue</t></si><si><t>Q1 total</t></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData>` +
			`<row><c t="s"><v>0</v></c><c t="s"><v>1</v></c></row>` +
			`<row><c><v>42</v></c></row>` +
			`</sheetData></worksheet>`,
	})
	res, err := NewXlsxExtractor().Extract("book.xlsx", data)
	require.NoError(t, err)

	file, sheets := splitNodes(res.Nodes)
	require.NotNil(t, file)
	require.Equal(t, 1, file.Meta["sheets"])
	require.Len(t, sheets, 1)
	require.Equal(t, "sheet_region", sheets[0].Meta["asset_kind"])
	txt, _ := sheets[0].Meta["section_text"].(string)
	require.Contains(t, txt, "Revenue")
	require.Contains(t, txt, "Q1 total")
	require.Contains(t, txt, "42")
}

func TestXlsxExtractor_InlineString(t *testing.T) {
	data := buildZip(t, map[string]string{
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData>` +
			`<row><c t="inlineStr"><is><t>inline cell</t></is></c></row>` +
			`</sheetData></worksheet>`,
	})
	res, err := NewXlsxExtractor().Extract("book.xlsx", data)
	require.NoError(t, err)
	_, sheets := splitNodes(res.Nodes)
	require.Len(t, sheets, 1)
	txt, _ := sheets[0].Meta["section_text"].(string)
	require.Contains(t, txt, "inline cell")
}
