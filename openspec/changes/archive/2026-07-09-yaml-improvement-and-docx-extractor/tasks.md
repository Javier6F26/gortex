## 1. DOCX Extractor — Foundation

- [x] 1.1 Create `internal/parser/languages/docx.go` with `DocxExtractor` struct implementing `Extractor`, `StreamingExtractor`, and `AssetExtractor`
- [x] 1.2 Implement ZIP reader: open `.docx` as `zip.Reader`, locate `word/document.xml`
- [x] 1.3 Implement streaming XML decoder for `word/document.xml` using `xml.NewDecoder`
- [x] 1.4 Add tests with a real `.docx` fixture (use reference document) and malformed input

## 2. DOCX Extractor — Sectioning & Content

- [x] 2.1 Detect `<w:pStyle w:val="HeadingN"/>` paragraphs as section boundaries, map heading level (1–9)
- [x] 2.2 Accumulate `<w:t>` text content per section from `<w:p>` → `<w:r>` hierarchy
- [x] 2.3 Extract table cell `<w:t>` text within sections (via `<w:tbl>` → `<w:tr>` → `<w:tc>` → `<w:p>`)
- [x] 2.4 Count images via `<wp:inline>` and `<wp:anchor>` blip references, record in file node Meta
- [x] 2.5 Emit one `KindDoc` per heading-delimited section with `section_text`, `heading_path`, `heading_level` in Meta
- [x] 2.6 Apply `contentSectionCap = 4000` truncation per section

## 3. DOCX Extractor — Registration

- [x] 3.1 Register `DocxExtractor` in `internal/parser/languages/register.go` (`RegisterAll`)
- [x] 3.2 Verify `.docx` extension resolves correctly via `DetectLanguageContent`
- [x] 3.3 Build and run existing tests to confirm no regressions

## 4. YAML Extractor — Block Sequence Extraction

- [x] 4.1 Modify `findTopLevelPairs` in `internal/parser/languages/yaml.go` to descend into `block_sequence` children
- [x] 4.2 Extract scalar `block_sequence_item` values (e.g. `- main`) as `KindVariable`
- [x] 4.3 Extract `block_mapping` items within sequence (e.g. `- name: ... repo: ...`) as `KindDoc` with metadata
- [x] 4.4 Generate deterministic node IDs based on parent key + item name/value (`file.yaml::services::ave-ai-vault`)
- [x] 4.5 Verify existing `TestYAMLExtractor_TopLevelKeys` and other YAML tests still pass

## 5. Verification

- [x] 5.1 Run full test suite: `go test -race ./internal/parser/languages/...`
- [x] 5.2 Build binary: `go build -o gortex ./cmd/gortex/`
- [x] 5.3 Run end-to-end: index a `.docx` and a `.yaml` with sequences, verify nodes appear in graph
