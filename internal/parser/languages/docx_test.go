package languages

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

// minDocx builds a minimal .docx from a document.xml body fragment.
// The body fragment is wrapped in the required OOXML structure (document
// element, body, namespace declarations).
func minDocx(bodyXML string) []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	// [Content_Types].xml — declare all needed types.
	_ = w.SetComment("")
	ct, _ := w.Create("[Content_Types].xml")
	_, _ = ct.Write([]byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`))

	// _rels/.rels — root relationship to document.xml.
	rels, _ := w.Create("_rels/.rels")
	_, _ = rels.Write([]byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`))

	// word/_rels/document.xml.rels — empty for no images.
	wcrels, _ := w.Create("word/_rels/document.xml.rels")
	_, _ = wcrels.Write([]byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
</Relationships>`))

	// word/document.xml — the main content.
	doc, _ := w.Create("word/document.xml")
	_, _ = doc.Write([]byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"
            xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing"
            xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
            xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <w:body>` + bodyXML + `</w:body>
</w:document>`))

	w.Close()
	return buf.Bytes()
}

func TestDocxExtractor_FileNode(t *testing.T) {
	src := minDocx(`<w:p><w:r><w:t>Hello</w:t></w:r></w:p>`)
	e := NewDocxExtractor()
	result, err := e.Extract("test.docx", src)
	if err != nil {
		t.Fatal(err)
	}
	files := nodesOfKind(result.Nodes, graph.KindFile)
	if len(files) != 1 {
		t.Fatalf("expected 1 KindFile node, got %d", len(files))
	}
	if files[0].Language != "docx" {
		t.Errorf("file language = %q, want docx", files[0].Language)
	}
}

func TestDocxExtractor_SectionsByHeading(t *testing.T) {
	src := minDocx(`
<w:p>
  <w:pPr><w:pStyle w:val="Heading1"/></w:pPr>
  <w:r><w:t>Main Title</w:t></w:r>
</w:p>
<w:p>
  <w:r><w:t>Paragraph under heading 1.</w:t></w:r>
</w:p>
<w:p>
  <w:pPr><w:pStyle w:val="Heading2"/></w:pPr>
  <w:r><w:t>Sub Section</w:t></w:r>
</w:p>
<w:p>
  <w:r><w:t>Text under sub section.</w:t></w:r>
</w:p>`)

	e := NewDocxExtractor()
	result, err := e.Extract("sections.docx", src)
	if err != nil {
		t.Fatal(err)
	}

	docs := nodesOfKind(result.Nodes, graph.KindDoc)
	if len(docs) != 2 {
		t.Fatalf("expected 2 KindDoc sections (Main Title + Sub Section), got %d", len(docs))
	}

	// First section: Main Title
	if docs[0].Meta["heading_level"] != 1 {
		t.Errorf("first section heading_level = %v, want 1", docs[0].Meta["heading_level"])
	}
	text0, _ := docs[0].Meta["section_text"].(string)
	if !strings.Contains(text0, "Paragraph under heading 1") {
		t.Errorf("first section should contain the paragraph text, got %q", text0)
	}

	// Second section: Sub Section
	if docs[1].Meta["heading_level"] != 2 {
		t.Errorf("second section heading_level = %v, want 2", docs[1].Meta["heading_level"])
	}
	text1, _ := docs[1].Meta["section_text"].(string)
	if !strings.Contains(text1, "Text under sub section") {
		t.Errorf("second section should contain the sub-section text, got %q", text1)
	}
}

func TestDocxExtractor_HeadingPath(t *testing.T) {
	src := minDocx(`
<w:p>
  <w:pPr><w:pStyle w:val="Heading1"/></w:pPr>
  <w:r><w:t>Chapter 1</w:t></w:r>
</w:p>
<w:p>
  <w:r><w:t>Some introductory text.</w:t></w:r>
</w:p>
<w:p>
  <w:pPr><w:pStyle w:val="Heading2"/></w:pPr>
  <w:r><w:t>Section 1.1</w:t></w:r>
</w:p>
<w:p>
  <w:r><w:t>Content text.</w:t></w:r>
</w:p>`)

	e := NewDocxExtractor()
	result, err := e.Extract("chapter.docx", src)
	if err != nil {
		t.Fatal(err)
	}

	docs := nodesOfKind(result.Nodes, graph.KindDoc)
	if len(docs) < 2 {
		t.Fatalf("expected >= 2 KindDoc sections, got %d", len(docs))
	}

	// The H1 section
	if docs[0].Meta["heading_level"] != 1 {
		t.Errorf("first heading_level = %v, want 1", docs[0].Meta["heading_level"])
	}
	path0, _ := docs[0].Meta["heading_path"].([]string)
	if len(path0) != 1 || path0[0] != "Chapter 1" {
		t.Errorf("heading_path = %v, want [Chapter 1]", path0)
	}

	// The H2 section — its heading_path should include the parent heading
	h2sec := docs[1]
	if h2sec.Meta["heading_level"] != 2 {
		t.Errorf("H2 heading_level = %v, want 2", h2sec.Meta["heading_level"])
	}
	path1, _ := h2sec.Meta["heading_path"].([]string)
	if len(path1) != 2 || path1[0] != "Chapter 1" || path1[1] != "Section 1.1" {
		t.Errorf("heading_path = %v, want [Chapter 1 Section 1.1]", path1)
	}
}

func TestDocxExtractor_GracefulOnGarbage(t *testing.T) {
	e := NewDocxExtractor()
	result, err := e.Extract("garbage.docx", []byte("not a zip file at all"))
	if err != nil {
		t.Fatal(err)
	}
	files := nodesOfKind(result.Nodes, graph.KindFile)
	if len(files) != 1 {
		t.Fatalf("expected 1 KindFile node even on garbage, got %d", len(files))
	}
}

func TestDocxExtractor_ExtractStreamInterface(t *testing.T) {
	src := minDocx(`<w:p><w:r><w:t>Stream test</w:t></w:r></w:p>`)
	e := NewDocxExtractor()

	var nodes []*graph.Node
	err := e.ExtractStream("stream.docx", bytes.NewReader(src), int64(len(src)),
		func(n *graph.Node, edges []*graph.Edge) {
			if n != nil {
				nodes = append(nodes, n)
			}
		})
	if err != nil {
		t.Fatal(err)
	}

	hasFile := false
	hasDoc := false
	for _, n := range nodes {
		switch n.Kind {
		case graph.KindFile:
			hasFile = true
		case graph.KindDoc:
			hasDoc = true
		}
	}
	if !hasFile {
		t.Error("ExtractStream should emit a KindFile node")
	}
	if !hasDoc {
		t.Error("ExtractStream should emit KindDoc nodes for section text")
	}
}

func TestDocxExtractor_ContentCap(t *testing.T) {
	// Build a long paragraph of "A" characters to exceed contentSectionCap.
	longText := strings.Repeat("A", contentSectionCap+1000)
	body := `<w:p>
  <w:pPr><w:pStyle w:val="Heading1"/></w:pPr>
  <w:r><w:t>` + longText + `</w:t></w:r>
</w:p>`
	src := minDocx(body)

	e := NewDocxExtractor()
	result, err := e.Extract("capped.docx", src)
	if err != nil {
		t.Fatal(err)
	}

	docs := nodesOfKind(result.Nodes, graph.KindDoc)
	if len(docs) == 0 {
		t.Fatal("expected at least 1 KindDoc")
	}
	text, _ := docs[0].Meta["section_text"].(string)
	if len(text) > contentSectionCap+100 {
		t.Errorf("section_text length = %d, want <= %d", len(text), contentSectionCap)
	}
}

func TestDocxExtractor_ExtensionRegistration(t *testing.T) {
	// Verify that the docx extension maps to the correct language
	// via the parser registry, as registered in RegisterAll.
	e := NewDocxExtractor()
	exts := e.Extensions()
	found := false
	for _, ext := range exts {
		if ext == ".docx" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal(".docx not in DocxExtractor.Extensions()")
	}
}

func TestDocxExtractor_MultipleRunsConcatenated(t *testing.T) {
	// Verify that text from multiple <w:r> runs in the same paragraph
	// is concatenated in order.
	src := minDocx(`<w:p>
  <w:r><w:t>First </w:t></w:r>
  <w:r><w:t>Second </w:t></w:r>
  <w:r><w:t>Third</w:t></w:r>
</w:p>`)

	e := NewDocxExtractor()
	result, err := e.Extract("runs.docx", src)
	if err != nil {
		t.Fatal(err)
	}

	docs := nodesOfKind(result.Nodes, graph.KindDoc)
	if len(docs) == 0 {
		t.Fatal("expected at least 1 KindDoc")
	}
	text, _ := docs[0].Meta["section_text"].(string)
	expected := "First Second Third"
	if !strings.Contains(text, expected) {
		t.Errorf("section_text should contain %q, got %q", expected, text)
	}
}
