package languages

import (
	"bufio"
	"bytes"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// TextExtractor ingests plain-text documents as content: one KindFile node plus
// one KindDoc "section" node per ~contentSectionCap-sized window, so long text
// is chunked for prose search instead of indexed as a single blob.
type TextExtractor struct{}

func NewTextExtractor() *TextExtractor { return &TextExtractor{} }

func (e *TextExtractor) Language() string     { return "text" }
func (e *TextExtractor) Extensions() []string { return []string{".txt", ".text"} }

var _ parser.StreamingExtractor = (*TextExtractor)(nil)

func (e *TextExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	res := &parser.ExtractionResult{}
	emitText(filePath, bytes.NewReader(src), int64(len(src)), collectInto(res))
	return res, nil
}

func (e *TextExtractor) ExtractStream(filePath string, r io.ReaderAt, size int64, emit func(*graph.Node, []*graph.Edge)) error {
	emitText(filePath, r, size, emit)
	return nil
}

func emitText(filePath string, r io.ReaderAt, size int64, emit func(*graph.Node, []*graph.Edge)) {
	fileNode := contentFileNode(filePath, "text", size)
	emit(fileNode, nil)

	sc := bufio.NewScanner(io.NewSectionReader(r, 0, size))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20) // tolerate long lines up to 4 MiB

	var b strings.Builder
	chunk := 0
	startLine := 1
	line := 0
	flush := func() {
		text := strings.TrimSpace(b.String())
		b.Reset()
		if text == "" {
			return
		}
		chunk++
		if len(text) > contentSectionCap {
			text = text[:contentSectionCap]
		}
		id := filePath + "::doc:section-" + strconv.Itoa(chunk)
		node := &graph.Node{
			ID: id, Kind: graph.KindDoc,
			Name:     path.Base(filePath) + " §" + strconv.Itoa(chunk),
			FilePath: filePath, StartLine: startLine, Language: "text",
			Meta: map[string]any{"asset_kind": "section", "ordinal": chunk, "section_text": text},
		}
		emit(node, []*graph.Edge{{From: filePath, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: startLine}})
	}

	for sc.Scan() {
		line++
		l := sc.Text()
		if b.Len() > 0 && b.Len()+len(l)+1 > contentSectionCap {
			flush()
			startLine = line
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(l)
	}
	flush()
}
