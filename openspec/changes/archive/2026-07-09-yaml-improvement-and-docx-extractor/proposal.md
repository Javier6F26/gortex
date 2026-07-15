## Why

El extractor YAML actual solo extrae claves de primer nivel como `KindVariable`, ignorando por completo listas (`block_sequence`) y pares anidados dentro de items. Archivos como `service-map.yaml` (con 77 servicios en lista) generan 0 nodos de contenido. Además, no existe un extractor para documentos Word (`.docx`), a pesar de que el codebase ya tiene el patrón completo para formatos Office (PPTX, XLSX) con `StreamingExtractor`, `AssetExtractor`, y `KindDoc` para búsqueda semántica de contenido. Ambos extractors son necesarios para que los vaults de documentación y configuración sean completamente indexables.

## What Changes

- **YAML extractor mejorado**: extraer `block_sequence` items, pares anidados dentro de listas, y valores escalares en secuencias como nodos `KindVariable`/`KindDoc`
- **DOCX extractor nuevo**: indexar documentos Word como `KindDoc` seccionados por headings, con texto completo para búsqueda semántica
- **Ninguno de los dos cambios rompe extractores existentes** — son puramente aditivos

## Capabilities

### New Capabilities
- `docx-extractor`: Indexación de documentos Word (.docx) como KindDoc por sección (heading), con texto completo para prose search
- `yaml-sequence-extraction`: Extracción de block_sequence y block_sequence_item en archivos YAML como nodos indexables

### Modified Capabilities
- *(ninguno — solo se añaden capacidades nuevas)*

## Impact

- **Archivos nuevos**: `internal/parser/languages/docx.go`, tests
- **Archivos modificados**: `internal/parser/languages/yaml.go` (+ tests), `internal/parser/languages/register.go` (registrar DocxExtractor)
- **Sin impacto en API, dependencias externas o schemas de DB** — los nuevos nodos KindDoc siguen el patrón existente
- **La infraestructura reutilizable** (`office.go`, `extractor.go`) ya está en su lugar
