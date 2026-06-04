package languages

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"path"
	"regexp"
	"strconv"
	"strings"

	// Raster decoders registered for image.DecodeConfig — stdlib formats
	// plus the x/image extras already vendored. DecodeConfig reads only
	// the header, so this is cheap even for large images.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/ledongthuc/pdf"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// --- DCA10: multimodal ingest (PDF + images) -----------------------
//
// Code and markdown were the only file classes the graph ingested. The
// two extractors below make image assets and PDF documents first-class
// nodes: an image becomes a KindImage asset node carrying its format and
// dimensions, and a PDF becomes a KindFile node with one searchable
// KindDoc node per page, so a diagram or a spec PDF is discoverable
// alongside the code it documents.

// ImageAssetExtractor ingests raster / vector image files as graph nodes.
type ImageAssetExtractor struct{}

func NewImageAssetExtractor() *ImageAssetExtractor { return &ImageAssetExtractor{} }

func (e *ImageAssetExtractor) Language() string { return "image" }
func (e *ImageAssetExtractor) Extensions() []string {
	return []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tiff", ".tif", ".ico", ".svg"}
}

var svgOpenRe = regexp.MustCompile(`(?is)<svg\b[^>]*>`)
var svgAttrRe = regexp.MustCompile(`(?i)\b(width|height)\s*=\s*["']?\s*([0-9.]+)`)
var svgViewBoxRe = regexp.MustCompile(`(?is)\bviewBox\s*=\s*["']\s*[0-9.]+\s+[0-9.]+\s+([0-9.]+)\s+([0-9.]+)`)

func (e *ImageAssetExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	result := &parser.ExtractionResult{}
	base := path.Base(filePath)
	ext := strings.ToLower(path.Ext(filePath))
	format := strings.TrimPrefix(ext, ".")

	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, Language: "image",
	}
	result.Nodes = append(result.Nodes, fileNode)

	sum := sha256.Sum256(src)
	meta := map[string]any{
		"asset_kind": "image",
		"format":     format,
		"size_bytes": len(src),
		"sha256":     hex.EncodeToString(sum[:]),
	}
	var w, h int
	if ext == ".svg" {
		w, h = svgDimensions(src)
		format = "svg"
	} else if cfg, decFormat, err := image.DecodeConfig(bytes.NewReader(src)); err == nil {
		w, h = cfg.Width, cfg.Height
		if decFormat != "" {
			format = decFormat
		}
	}
	meta["format"] = format
	if w > 0 {
		meta["width"] = w
	}
	if h > 0 {
		meta["height"] = h
	}

	imgID := "image::asset::" + filePath
	result.Nodes = append(result.Nodes, &graph.Node{
		ID: imgID, Kind: graph.KindImage, Name: base,
		FilePath: filePath, StartLine: 1, Language: "image", Meta: meta,
	})
	result.Edges = append(result.Edges, &graph.Edge{
		From: filePath, To: imgID, Kind: graph.EdgeDefines, FilePath: filePath, Line: 1,
	})
	return result, nil
}

// svgDimensions parses an SVG's intrinsic size from the width / height
// attributes of its opening <svg> tag, falling back to the viewBox's
// w/h. Returns 0,0 when absent.
func svgDimensions(src []byte) (w, h int) {
	tag := svgOpenRe.Find(src)
	if tag == nil {
		return 0, 0
	}
	for _, m := range svgAttrRe.FindAllSubmatch(tag, -1) {
		v := atoiFloor(string(m[2]))
		switch strings.ToLower(string(m[1])) {
		case "width":
			if w == 0 {
				w = v
			}
		case "height":
			if h == 0 {
				h = v
			}
		}
	}
	if w == 0 || h == 0 {
		if m := svgViewBoxRe.FindSubmatch(tag); m != nil {
			if w == 0 {
				w = atoiFloor(string(m[1]))
			}
			if h == 0 {
				h = atoiFloor(string(m[2]))
			}
		}
	}
	return w, h
}

// atoiFloor parses the leading integer part of a numeric string, ignoring
// a fractional part and any trailing unit (e.g. "48px" -> 48, "12.5" -> 12).
func atoiFloor(s string) int {
	s = strings.TrimSpace(s)
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, _ := strconv.Atoi(s[:end])
	return n
}

// PDFExtractor ingests PDF documents: one KindFile node carrying page
// count + size, plus one KindDoc node per page whose extracted text feeds
// the prose search index.
type PDFExtractor struct{}

func NewPDFExtractor() *PDFExtractor { return &PDFExtractor{} }

func (e *PDFExtractor) Language() string     { return "pdf" }
func (e *PDFExtractor) Extensions() []string { return []string{".pdf"} }

// pdfPageTextCap bounds the per-page text stored on a KindDoc node so a
// large PDF can't bloat the graph; the head of each page is enough for
// prose search to locate the right document.
const pdfPageTextCap = 4000

func (e *PDFExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	result := &parser.ExtractionResult{}
	sum := sha256.Sum256(src)
	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, Language: "pdf",
		Meta: map[string]any{
			"asset_kind": "pdf",
			"size_bytes": len(src),
			"sha256":     hex.EncodeToString(sum[:]),
		},
	}
	result.Nodes = append(result.Nodes, fileNode)

	// The PDF reader can panic on malformed / encrypted documents — keep
	// extraction failures from killing the whole index by recovering and
	// returning just the file node.
	func() {
		defer func() { _ = recover() }()
		r, err := pdf.NewReader(bytes.NewReader(src), int64(len(src)))
		if err != nil || r == nil {
			return
		}
		pages := r.NumPage()
		if pages > 0 {
			fileNode.Meta["pages"] = pages
		}
		for i := 1; i <= pages; i++ {
			text := pdfPageText(r, i)
			if text == "" {
				continue
			}
			if len(text) > pdfPageTextCap {
				text = text[:pdfPageTextCap]
			}
			pageID := filePath + "::doc:page-" + strconv.Itoa(i)
			result.Nodes = append(result.Nodes, &graph.Node{
				ID: pageID, Kind: graph.KindDoc, Name: path.Base(filePath) + " p." + strconv.Itoa(i),
				FilePath: filePath, StartLine: i, Language: "pdf",
				Meta: map[string]any{"asset_kind": "pdf_page", "page": i, "section_text": text},
			})
			result.Edges = append(result.Edges, &graph.Edge{
				From: filePath, To: pageID, Kind: graph.EdgeDefines, FilePath: filePath, Line: i,
			})
		}
	}()
	return result, nil
}

// pdfPageText extracts and whitespace-collapses one page's plain text,
// recovering from the reader's panics on a per-page basis.
func pdfPageText(r *pdf.Reader, page int) (text string) {
	defer func() { _ = recover() }()
	p := r.Page(page)
	if p.V.IsNull() {
		return ""
	}
	raw, err := p.GetPlainText(nil)
	if err != nil {
		return ""
	}
	return strings.Join(strings.Fields(raw), " ")
}
