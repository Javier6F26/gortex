package languages

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strconv"
	"strings"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// DocxExtractor ingests Word documents (.docx): one KindFile node plus one
// KindDoc node per heading-delimited section, for prose search and
// breadcrumb navigation. Follows the same pattern as PptxExtractor /
// XlsxExtractor in office.go, with heading-based sectioning inspired by
// markdown_prose.go.
type DocxExtractor struct{}

func NewDocxExtractor() *DocxExtractor { return &DocxExtractor{} }

func (e *DocxExtractor) Language() string              { return "docx" }
func (e *DocxExtractor) Extensions() []string          { return []string{".docx"} }
func (e *DocxExtractor) AssetClass() parser.AssetClass { return parser.AssetDocument }

var (
	_ parser.StreamingExtractor = (*DocxExtractor)(nil)
	_ parser.AssetExtractor     = (*DocxExtractor)(nil)
)

func (e *DocxExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	res := &parser.ExtractionResult{}
	emitDocx(filePath, bytes.NewReader(src), int64(len(src)), collectInto(res))
	return res, nil
}

func (e *DocxExtractor) ExtractStream(filePath string, r io.ReaderAt, size int64, emit func(*graph.Node, []*graph.Edge)) error {
	emitDocx(filePath, r, size, emit)
	return nil
}

// docxHeadingStyle maps a w:pStyle val to a heading level (1..9) or 0.
var docxHeadingStyle = func() map[string]int {
	m := make(map[string]int, 9)
	for i := 1; i <= 9; i++ {
		m["Heading"+strconv.Itoa(i)] = i
	}
	return m
}()

// docxSection accumulates one heading-delimited region.
type docxSection struct {
	level  int      // heading depth (1..9); 0 = preamble before first heading
	crumbs []string // heading-path breadcrumb, root-first
	body   strings.Builder
}

// wNamespace is the OOXML WordprocessingML namespace URI used in document.xml.
const wNamespace = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"

func emitDocx(filePath string, r io.ReaderAt, size int64, emit func(*graph.Node, []*graph.Edge)) {
	fileNode := contentFileNode(filePath, "docx", size)
	emit(fileNode, nil)

	zr, err := zip.NewReader(r, size)
	if err != nil {
		return
	}

	docEntry := findZipEntry(zr, "word/document.xml")
	relsEntry := findZipEntry(zr, "word/_rels/document.xml.rels")
	if docEntry == nil {
		return
	}

	// Count images by scanning raw content for <wp:inline> / <wp:anchor> tags.
	imgCount := docxCountImages(docEntry)

	// Extract image relationship names for metadata.
	imageRels := docxParseImageRels(relsEntry)

	// Stream through document.xml paragraph by paragraph.
	sections := docxExtractSections(docEntry)
	if len(sections) == 0 {
		return
	}

	if imgCount > 0 {
		fileNode.Meta["image_count"] = imgCount
	}
	if imageRels != "" {
		fileNode.Meta["image_rels"] = imageRels
	}

	base := proseFileBase(filePath)
	seen := make(map[string]bool, len(sections))
	for _, sec := range sections {
		body := strings.TrimSpace(sec.body.String())
		if body == "" {
			continue
		}
		if len(body) > contentSectionCap {
			body = body[:contentSectionCap]
		}

		// Build stable section ID from heading path.
		id := filePath + "::doc:" + idSlug(base)
		for _, c := range sec.crumbs {
			if s := idSlug(c); s != "" {
				id += "-" + s
			}
		}
		// Deduplicate identical heading paths.
		if seen[id] {
			continue
		}
		seen[id] = true

		node := &graph.Node{
			ID:        id,
			Kind:      graph.KindDoc,
			Name:      proseBreadcrumb(base, sec.crumbs),
			FilePath:  filePath,
			StartLine: 1,
			Language:  "docx",
			Meta: map[string]any{
				"section_text":  body,
				"heading_path":  sec.crumbs,
				"heading_level": sec.level,
			},
		}
		emit(node, []*graph.Edge{
			{From: filePath, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: 1},
		})
	}
}

// docxCountImages scans the raw document.xml bytes for DrawingML image
// container elements and returns the count.
func docxCountImages(f *zip.File) int {
	rc, err := f.Open()
	if err != nil {
		return 0
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		return 0
	}
	// Each embedded image is wrapped in <wp:inline> or <wp:anchor>.
	count := bytes.Count(data, []byte("<wp:inline"))
	count += bytes.Count(data, []byte("<wp:anchor"))
	return count
}

// docxParseImageRels reads word/_rels/document.xml.rels and returns a
// comma-separated list of image target basenames.
func docxParseImageRels(f *zip.File) string {
	if f == nil {
		return ""
	}
	rc, err := f.Open()
	if err != nil {
		return ""
	}
	defer func() { _ = rc.Close() }()

	dec := xml.NewDecoder(rc)
	var images []string
	for {
		tok, terr := dec.Token()
		if terr != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "Relationship" {
			continue
		}
		var target, typ string
		for _, a := range se.Attr {
			if a.Name.Local == "Target" {
				target = a.Value
			}
			if a.Name.Local == "Type" {
				typ = a.Value
			}
		}
		if strings.Contains(typ, "image") && target != "" {
			if idx := strings.LastIndexByte(target, '/'); idx >= 0 {
				target = target[idx+1:]
			}
			images = append(images, target)
		}
	}
	return strings.Join(images, ", ")
}

// docxExtractSections streams word/document.xml paragraph by paragraph
// and returns heading-delimited sections with accumulated text.
func docxExtractSections(f *zip.File) []*docxSection {
	rc, err := f.Open()
	if err != nil {
		return nil
	}
	defer func() { _ = rc.Close() }()

	dec := xml.NewDecoder(rc)

	var stack []*docxSection
	var finished []*docxSection

	addText := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		if len(stack) == 0 {
			stack = append(stack, &docxSection{level: 0})
		}
		top := stack[len(stack)-1]
		if top.body.Len() > 0 {
			top.body.WriteByte(' ')
		}
		top.body.WriteString(text)
	}

	closeTo := func(level int) {
		for len(stack) > 0 && stack[len(stack)-1].level >= level {
			finished = append(finished, stack[len(stack)-1])
			stack = stack[:len(stack)-1]
		}
	}

	inP := false
	var paraText strings.Builder
	paraHeading := 0

	for {
		tok, terr := dec.Token()
		if terr != nil {
			break
		}

		switch t := tok.(type) {
		case xml.StartElement:
			// Only process elements in the w: namespace.
			if t.Name.Space != wNamespace && t.Name.Space != "" {
				break
			}
			switch t.Name.Local {
			case "p":
				inP = true
				paraText.Reset()
				paraHeading = 0
			case "pStyle":
				if !inP {
					break
				}
				for _, a := range t.Attr {
					if a.Name.Local == "val" {
						if level, ok := docxHeadingStyle[a.Value]; ok {
							paraHeading = level
						}
					}
				}
			case "t":
				if !inP {
					break
				}
				var s string
				if dec.DecodeElement(&s, &t) == nil {
					paraText.WriteString(s)
				}
			}

		case xml.EndElement:
			if t.Name.Space != wNamespace && t.Name.Space != "" {
				break
			}
			switch t.Name.Local {
			case "p":
				inP = false
				text := strings.TrimSpace(paraText.String())
				if text == "" {
					continue
				}
				if paraHeading > 0 {
					closeTo(paraHeading)
					var crumbs []string
					if len(stack) > 0 {
						crumbs = append(crumbs, stack[len(stack)-1].crumbs...)
					}
					crumbs = append(crumbs, text)
					sec := &docxSection{
						level:  paraHeading,
						crumbs: crumbs,
					}
					sec.body.WriteString(text)
					stack = append(stack, sec)
				} else {
					addText(text)
				}
			}
		}
	}

	// Flush remaining open sections.
	closeTo(1)
	finished = append(finished, stack...)

	// closeTo pops deepest-first (LIFO); reverse so sections are in
	// document order (shallowest first).
	for i, j := 0, len(finished)-1; i < j; i, j = i+1, j-1 {
		finished[i], finished[j] = finished[j], finished[i]
	}
	return finished
}
