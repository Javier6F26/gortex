## ADDED Requirements

### Requirement: Parse DOCX as ZIP of XML
The DocxExtractor SHALL open .docx files as ZIP archives using `archive/zip` and parse `word/document.xml` using `encoding/xml` with a streaming decoder.

#### Scenario: Valid DOCX produces file node
- **WHEN** DocxExtractor.Extract receives a valid .docx byte slice
- **THEN** it returns an ExtractionResult with at least one KindFile node with Language "docx"

#### Scenario: Malformed DOCX returns file node without crash
- **WHEN** DocxExtractor.Extract receives garbage bytes
- **THEN** it returns an ExtractionResult with only the KindFile node and no error

### Requirement: Section by Word headings
The DocxExtractor SHALL detect `<w:pStyle w:val="HeadingN"/>` paragraphs as section boundaries and SHALL emit one KindDoc per heading-delimited region.

#### Scenario: Headings create sections
- **WHEN** a .docx contains paragraphs with Heading1, Heading2, and Heading3 styles
- **THEN** the extractor SHALL emit one KindDoc per heading, with each section containing the text between its heading and the next heading of equal or shallower level

#### Scenario: Section hierarchy in breadcrumb
- **WHEN** a KindDoc section is emitted for an H3 heading inside an H2 heading
- **THEN** the KindDoc.Meta["heading_path"] SHALL contain the full breadcrumb `["H2 Text", "H3 Text"]`

### Requirement: StreamingExtractor interface
The DocxExtractor SHALL implement `parser.StreamingExtractor` and `parser.AssetExtractor` returning `AssetDocument`.

#### Scenario: ExtractStream reads paragraph by paragraph
- **WHEN** ExtractStream receives a .docx file via io.ReaderAt
- **THEN** it SHALL emit nodes progressively without loading the full document.xml into memory

### Requirement: Text extraction from runs
The DocxExtractor SHALL extract text from `<w:t>` elements inside `<w:r>` (run) elements within each paragraph.

#### Scenario: Multi-run paragraph concatenation
- **WHEN** a paragraph contains multiple <w:r> runs with different formatting
- **THEN** the extracted section text SHALL concatenate all <w:t> values in document order

### Requirement: Table text inclusion
The DocxExtractor SHALL extract text from table cells (`<w:tc>`) within `<w:tbl>` elements as part of the section content.

#### Scenario: Table text in section
- **WHEN** a table exists within a heading-delimited section
- **THEN** the cell text SHALL be included in the section's `section_text`

### Requirement: Image metadata
The DocxExtractor SHALL count images referenced via `<wp:inline>` and `<wp:anchor>` and SHALL record the count and relationship IDs in the file node metadata.

#### Scenario: Image count recorded
- **WHEN** a .docx contains 33 embedded images
- **THEN** the KindFile node SHALL have Meta["image_count"] = 33

### Requirement: Content cap per section
The DocxExtractor SHALL apply `contentSectionCap` (4000 bytes) to each section's stored text, matching the existing PPTX/XLSX behaviour.

#### Scenario: Overly long section truncated
- **WHEN** a section's text exceeds 4000 bytes
- **THEN** the section_text SHALL be truncated to 4000 bytes

### Requirement: Register in language registry
The DocxExtractor SHALL be registered in `RegisterAll` so `.docx` files are routed to it during indexing.

#### Scenario: .docx extension registered
- **WHEN** the registry is populated via RegisterAll
- **THEN** `DetectLanguageContent("file.docx", nil)` returns ("docx", true)
