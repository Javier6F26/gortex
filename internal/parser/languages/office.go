package languages

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// contentSectionCap bounds the text stored on a content KindDoc node (slide /
// sheet / section) so a large document can't bloat the graph; the head is
// enough for prose search to locate it. Extraction stops once it is reached.
const contentSectionCap = 4000

// sharedStringsCap bounds the bytes accumulated from an xlsx shared-string
// table so a pathological workbook can't exhaust memory; references past the
// cap resolve to empty.
const sharedStringsCap = 8 << 20

// contentFileNode builds the KindFile node for a content document.
func contentFileNode(filePath, lang string, size int64) *graph.Node {
	return &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, Language: lang,
		Meta: map[string]any{"asset_kind": lang, "size_bytes": int(size)},
	}
}

// collectInto returns an emit sink that appends to res — the byte-path adapter
// that lets an emit-based content core also satisfy the []byte Extract path.
func collectInto(res *parser.ExtractionResult) func(*graph.Node, []*graph.Edge) {
	return func(n *graph.Node, edges []*graph.Edge) {
		if n != nil {
			res.Nodes = append(res.Nodes, n)
		}
		res.Edges = append(res.Edges, edges...)
	}
}

// contentChunkNode builds one content KindDoc chunk (slide / sheet / section)
// plus its defines edge from the owning file, with text capped.
func contentChunkNode(filePath, lang, assetKind, name string, ordinal int, text string) (*graph.Node, *graph.Edge) {
	if len(text) > contentSectionCap {
		text = text[:contentSectionCap]
	}
	id := filePath + "::doc:" + assetKind + "-" + strconv.Itoa(ordinal)
	node := &graph.Node{
		ID: id, Kind: graph.KindDoc, Name: name,
		FilePath: filePath, StartLine: ordinal, Language: lang,
		Meta: map[string]any{"asset_kind": assetKind, "ordinal": ordinal, "section_text": text},
	}
	edge := &graph.Edge{From: filePath, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: ordinal}
	return node, edge
}

// ooxmlPartNumber parses the trailing integer of an OOXML part name like
// "ppt/slides/slide12.xml" given the prefix "ppt/slides/slide".
func ooxmlPartNumber(name, prefix string) (int, bool) {
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".xml") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".xml"))
	if err != nil {
		return 0, false
	}
	return n, true
}

func findZipEntry(zr *zip.Reader, name string) *zip.File {
	for _, f := range zr.File {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// xmlElementText opens a zip entry and concatenates the character data of every
// element with the given local name, whitespace-collapsed and capped. Streaming:
// it stops once the cap is reached, so a huge part is never fully buffered.
func xmlElementText(f *zip.File, local string) string {
	rc, err := f.Open()
	if err != nil {
		return ""
	}
	defer func() { _ = rc.Close() }()
	dec := xml.NewDecoder(rc)
	var b strings.Builder
	for b.Len() < contentSectionCap {
		tok, terr := dec.Token()
		if terr != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != local {
			continue
		}
		var s string
		if dec.DecodeElement(&s, &se) == nil && s != "" {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(s)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// --- pptx -----------------------------------------------------------

// PptxExtractor ingests PowerPoint decks: one KindFile node plus one KindDoc
// node per slide, carrying the slide's visible text for prose search.
type PptxExtractor struct{}

func NewPptxExtractor() *PptxExtractor { return &PptxExtractor{} }

func (e *PptxExtractor) Language() string     { return "pptx" }
func (e *PptxExtractor) Extensions() []string { return []string{".pptx"} }

var _ parser.StreamingExtractor = (*PptxExtractor)(nil)

func (e *PptxExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	res := &parser.ExtractionResult{}
	emitPptx(filePath, bytes.NewReader(src), int64(len(src)), collectInto(res))
	return res, nil
}

func (e *PptxExtractor) ExtractStream(filePath string, r io.ReaderAt, size int64, emit func(*graph.Node, []*graph.Edge)) error {
	emitPptx(filePath, r, size, emit)
	return nil
}

func emitPptx(filePath string, r io.ReaderAt, size int64, emit func(*graph.Node, []*graph.Edge)) {
	fileNode := contentFileNode(filePath, "pptx", size)
	emit(fileNode, nil)
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return
	}
	type slide struct {
		num int
		f   *zip.File
	}
	var slides []slide
	for _, f := range zr.File {
		if n, ok := ooxmlPartNumber(f.Name, "ppt/slides/slide"); ok {
			slides = append(slides, slide{n, f})
		}
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].num < slides[j].num })
	if len(slides) > 0 {
		fileNode.Meta["slides"] = len(slides)
	}
	for _, s := range slides {
		text := xmlElementText(s.f, "t") // DrawingML <a:t> runs
		if text == "" {
			continue
		}
		node, edge := contentChunkNode(filePath, "pptx", "slide",
			path.Base(filePath)+" slide "+strconv.Itoa(s.num), s.num, text)
		emit(node, []*graph.Edge{edge})
	}
}

// --- xlsx -----------------------------------------------------------

// XlsxExtractor ingests Excel workbooks: one KindFile node plus one KindDoc
// node per worksheet, carrying the sheet's cell text (shared strings resolved).
type XlsxExtractor struct{}

func NewXlsxExtractor() *XlsxExtractor { return &XlsxExtractor{} }

func (e *XlsxExtractor) Language() string     { return "xlsx" }
func (e *XlsxExtractor) Extensions() []string { return []string{".xlsx"} }

var _ parser.StreamingExtractor = (*XlsxExtractor)(nil)

func (e *XlsxExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	res := &parser.ExtractionResult{}
	emitXlsx(filePath, bytes.NewReader(src), int64(len(src)), collectInto(res))
	return res, nil
}

func (e *XlsxExtractor) ExtractStream(filePath string, r io.ReaderAt, size int64, emit func(*graph.Node, []*graph.Edge)) error {
	emitXlsx(filePath, r, size, emit)
	return nil
}

func emitXlsx(filePath string, r io.ReaderAt, size int64, emit func(*graph.Node, []*graph.Edge)) {
	fileNode := contentFileNode(filePath, "xlsx", size)
	emit(fileNode, nil)
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return
	}
	shared := readSharedStrings(zr)
	type sheet struct {
		num int
		f   *zip.File
	}
	var sheets []sheet
	for _, f := range zr.File {
		if n, ok := ooxmlPartNumber(f.Name, "xl/worksheets/sheet"); ok {
			sheets = append(sheets, sheet{n, f})
		}
	}
	sort.Slice(sheets, func(i, j int) bool { return sheets[i].num < sheets[j].num })
	if len(sheets) > 0 {
		fileNode.Meta["sheets"] = len(sheets)
	}
	for _, s := range sheets {
		text := xlsxSheetText(s.f, shared)
		if text == "" {
			continue
		}
		node, edge := contentChunkNode(filePath, "xlsx", "sheet_region",
			path.Base(filePath)+" sheet "+strconv.Itoa(s.num), s.num, text)
		emit(node, []*graph.Edge{edge})
	}
}

// readSharedStrings loads the workbook's shared-string table (xl/sharedStrings.xml)
// into an index-addressed slice, bounded by sharedStringsCap.
func readSharedStrings(zr *zip.Reader) []string {
	f := findZipEntry(zr, "xl/sharedStrings.xml")
	if f == nil {
		return nil
	}
	rc, err := f.Open()
	if err != nil {
		return nil
	}
	defer func() { _ = rc.Close() }()
	dec := xml.NewDecoder(rc)
	var out []string
	var cur strings.Builder
	var total int
	inSI := false
	for total < sharedStringsCap {
		tok, terr := dec.Token()
		if terr != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSI = true
				cur.Reset()
			case "t":
				if inSI {
					var s string
					if dec.DecodeElement(&s, &t) == nil {
						cur.WriteString(s)
					}
				}
			}
		case xml.EndElement:
			if t.Name.Local == "si" {
				s := cur.String()
				total += len(s)
				out = append(out, s)
				inSI = false
			}
		}
	}
	return out
}

// xlsxSheetText streams a worksheet's cells, resolving shared-string references,
// and returns the concatenated text capped at contentSectionCap.
func xlsxSheetText(f *zip.File, shared []string) string {
	rc, err := f.Open()
	if err != nil {
		return ""
	}
	defer func() { _ = rc.Close() }()
	dec := xml.NewDecoder(rc)
	var b strings.Builder
	cellType := ""
	for b.Len() < contentSectionCap {
		tok, terr := dec.Token()
		if terr != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "c":
			cellType = ""
			for _, a := range se.Attr {
				if a.Name.Local == "t" {
					cellType = a.Value
				}
			}
		case "v":
			var s string
			if dec.DecodeElement(&s, &se) == nil {
				txt := s
				if cellType == "s" {
					if idx, cerr := strconv.Atoi(strings.TrimSpace(s)); cerr == nil && idx >= 0 && idx < len(shared) {
						txt = shared[idx]
					}
				}
				if txt != "" {
					if b.Len() > 0 {
						b.WriteByte(' ')
					}
					b.WriteString(txt)
				}
			}
		case "t":
			if cellType == "inlineStr" {
				var s string
				if dec.DecodeElement(&s, &se) == nil && s != "" {
					if b.Len() > 0 {
						b.WriteByte(' ')
					}
					b.WriteString(s)
				}
			}
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
