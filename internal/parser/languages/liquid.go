package languages

import (
	"regexp"
	"strings"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// Liquid (Shopify, Jekyll) uses `{% ... %}` for control flow. The
// extractor captures:
//   - `{% assign X = ... %}`  → variable nodes
//   - `{% capture NAME %}...{% endcapture %}` → function nodes
//   - `{% include 'x' %}` / `{% render 'x' %}` → import edges
var (
	// `-?` after `{%` / before `%}` accepts the whitespace-trimming tag forms
	// `{%- … -%}` that ship in real Shopify/Jekyll themes.
	liquidAssignRe  = regexp.MustCompile(`(?m)\{%-?\s*assign\s+([A-Za-z_][\w]*)\s*=`)
	liquidCaptureRe = regexp.MustCompile(`(?m)\{%-?\s*capture\s+([A-Za-z_][\w]*)\s*-?%\}`)
	liquidIncludeRe = regexp.MustCompile(`(?m)\{%-?\s*include\s+['"]([^'"]+)['"]`)
	liquidRenderRe  = regexp.MustCompile(`(?m)\{%-?\s*render\s+['"]([^'"]+)['"]`)
	liquidSectionRe = regexp.MustCompile(`(?m)\{%-?\s*section\s+['"]([^'"]+)['"]`)
	liquidSchemaRe  = regexp.MustCompile(`(?m)\{%-?\s*schema\s*-?%\}`)
)

// liquidSnippetPath / liquidSectionPath normalize a bare render/include/section
// name to the theme-relative file it resolves to (Shopify layout convention),
// so the import edge lands on a real cross-file target instead of a bare name.
// liquidBareName returns the searchable bare name of a render / include /
// section target — the last path segment ("components/card" → "card").
func liquidBareName(raw string) string {
	if i := strings.LastIndexByte(raw, '/'); i >= 0 {
		return raw[i+1:]
	}
	return raw
}

func liquidSnippetPath(name string) string { return liquidThemePath("snippets", name) }
func liquidSectionPath(name string) string { return liquidThemePath("sections", name) }

func liquidThemePath(dir, name string) string {
	name = strings.TrimSuffix(name, ".liquid")
	if strings.Contains(name, "/") {
		return name + ".liquid" // already a path
	}
	return dir + "/" + name + ".liquid"
}

// LiquidExtractor extracts Shopify/Jekyll Liquid templates.
type LiquidExtractor struct{}

func NewLiquidExtractor() *LiquidExtractor { return &LiquidExtractor{} }

func (e *LiquidExtractor) Language() string     { return "liquid" }
func (e *LiquidExtractor) Extensions() []string { return []string{".liquid"} }

func (e *LiquidExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	lines := strings.Split(string(src), "\n")
	result := &parser.ExtractionResult{}

	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: len(lines),
		Language: "liquid",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	add := func(name string, kind graph.NodeKind, start, end int) {
		if name == "" {
			return
		}
		id := filePath + "::" + name
		if seen[id] {
			return
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: kind, Name: name,
			FilePath: filePath, StartLine: start, EndLine: end,
			Language: "liquid",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	for _, m := range liquidAssignRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		add(name, graph.KindVariable, line, line)
	}
	for _, m := range liquidCaptureRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		add(name, graph.KindFunction, line, findKeywordBlockEnd(lines, line, "{% endcapture"))
	}

	// render / include reference a snippet; section references a section file —
	// normalize both to the theme-relative file so the import edge resolves
	// cross-file to the real partial rather than dangling on a bare name.
	emitImport := func(re *regexp.Regexp, normalize func(string) string, tag string) {
		for _, m := range re.FindAllSubmatchIndex(src, -1) {
			raw := string(src[m[2]:m[3]])
			mod := normalize(raw)
			line := lineAt(src, m[0])
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: "unresolved::import::" + mod,
				Kind: graph.EdgeImports, FilePath: filePath, Line: line,
			})
			// Mint a searchable import node for the usage site (one per target),
			// carrying the tag type so a `render` (isolated scope) is
			// distinguishable from an `include` (shared scope). The existing
			// cross-file EdgeImports resolution above is preserved.
			nodeID := filePath + "::import::" + mod
			if !seen[nodeID] {
				seen[nodeID] = true
				result.Nodes = append(result.Nodes, &graph.Node{
					ID: nodeID, Kind: graph.KindImport, Name: liquidBareName(raw),
					FilePath: filePath, StartLine: line, EndLine: line, Language: "liquid",
					Meta: map[string]any{"liquid_tag": tag, "target": mod},
				})
				result.Edges = append(result.Edges, &graph.Edge{
					From: fileNode.ID, To: nodeID, Kind: graph.EdgeDefines,
					FilePath: filePath, Line: line,
				})
			}
		}
	}
	emitImport(liquidIncludeRe, liquidSnippetPath, "include")
	emitImport(liquidRenderRe, liquidSnippetPath, "render")
	emitImport(liquidSectionRe, liquidSectionPath, "section")

	// A `{% schema %} … {% endschema %}` block configures a section's settings.
	// Record that it exists as a redacted constant — never the raw JSON, which
	// routinely carries credentials / API keys / store-specific values.
	for _, m := range liquidSchemaRe.FindAllSubmatchIndex(src, -1) {
		line := lineAt(src, m[0])
		id := filePath + "::schema"
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindConstant, Name: "schema",
			FilePath: filePath, StartLine: line,
			EndLine:  findKeywordBlockEnd(lines, line, "{%- endschema"),
			Language: "liquid",
			Meta:     map[string]any{"liquid_schema": true, "value_redacted": true},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: line,
		})
	}

	return result, nil
}

var _ parser.Extractor = (*LiquidExtractor)(nil)
