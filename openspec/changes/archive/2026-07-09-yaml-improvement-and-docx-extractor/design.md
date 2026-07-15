## Context

Gortex indexa repositorios en un grafo de conocimiento con 120+ extractores de lenguajes. Actualmente:

- **YAML extractor** (`internal/parser/languages/yaml.go`): usa tree-sitter, solo extrae `block_mapping_pair` del toplevel como `KindVariable`. Ignora `block_sequence` y pares anidados en listas.
- **Office extractors** (`internal/parser/languages/office.go`): existen PptxExtractor y XlsxExtractor que implementan `StreamingExtractor` + `AssetExtractor` (AssetDocument). No hay DocxExtractor.
- **Markdown prose section extractor** (`internal/parser/languages/markdown_prose.go`): secciona documentos por headings, emitiendo 1 `KindDoc` por sección con texto completo para búsqueda semántica. El DOCX debe seguir exactamente este patrón.

El codebase ya tiene `gopkg.in/yaml.v3` como dependencia (usado por `ansible.go`) y toda la infraestructura para office docs en `office.go`.

## Goals / Non-Goals

**Goals:**
- Extraer items de `block_sequence` en YAML como nodos indexables (`KindVariable`/`KindDoc`)
- Extraer pares anidados dentro de items de lista
- Indexar documentos .docx con seccionado por headings, texto completo para prose search
- Reutilizar al máximo la infraestructura existente (`office.go`, `StreamingExtractor`, `AssetExtractor`)

**Non-Goals:**
- No reemplazar el motor de parseo tree-sitter por yaml.v3 en el YAML genérico (se mejora el tree-sitter actual para evitar regresiones)
- No extraer estilos visuales ni formato del DOCX (solo texto plano)
- No crear un extractor semántico específico para service-map (queda para futuro)

## Decisions

### D1: YAML — Mejorar tree-sitter existente (Camino A) en vez de migrar a yaml.v3

| Opción | Riesgo |
|--------|--------|
| **Camino A**: mejorar tree-sitter actual | Riesgo cero — puramente aditivo |
| **Camino B**: migrar a yaml.v3 como Ansible | Posibles diferencias de parseo en casos extremos (tabs, anchors) |

El Camino A garantiza que todos los archivos YAML existentes sigan extrayéndose exactamente igual. Los nodos nuevos son adicionales, nunca sustitutivos. La función `findTopLevelPairs` se modifica para que cuando encuentre un `block_mapping_pair` cuyo valor sea un `block_sequence`, recorra los `block_sequence_item` hijos.

### D2: DOCX — Patrón idéntico a PPTX/XLSX + seccionado tipo Markdown

| Aspecto | Decisión |
|---------|----------|
| Parseo | `encoding/xml` + zip reader (exactamente como office.go) |
| Streaming | Implementar `StreamingExtractor` para lectura eficiente |
| Asset class | `AssetDocument` (igual que PDF, PPTX, XLSX) |
| Seccionado | Por `<w:pStyle>` = Heading1..Heading9 (igual que markdown_prose.go con atx_heading) |
| Texto | Extraer `<w:t>` dentro de `<w:r>` (runs) — mismo approach que PPTX con `<a:t>` |
| Tablas | Extraer texto de celdas como contenido de sección |
| Imágenes | Conteo + metadatos de relaciones, no extracción de píxeles |
| Size cap | `contentSectionCap = 4000` chars por sección (reutilizado de office.go) |

### D3: No dependencias externas nuevas

`encoding/xml` y `archive/zip` son parte de la stdlib de Go. No se requiere ninguna librería OOXML adicional — el XML del DOCX es suficientemente simple para parsearlo con `xml.NewDecoder` streaming.

## Risks / Trade-offs

- **Rendimiento DOCX**: El document.xml del ejemplo pesa 2.3 MB. Con decoder streaming y `contentSectionCap`, el pico de memoria es O(el chunk más grande), no O(el archivo completo). El patrón PPTX/XLSX ya valida esto.
- **Headings sin estilo Word**: Si un documento usa numbering manual (1., 1.1) sin estilos Heading, no se detectarán como secciones. Se puede mitigar detectando también `w:numPr` con `w:numId` que apunte a formato decimal.
- **Anidamiento profundo YAML**: La nueva walk tree-sitter debe tener un límite de profundidad (el actual es `depth <= 5`) para evitar stack overflow accidental. El límite actual es suficiente para service-map.
- **Nodos duplicados en YAML**: Si el mismo nombre aparece en múltiples listas, los IDs basados en path (ej: `file.yaml::services::ave-backoffice`) garantizan unicidad sin colisión.
