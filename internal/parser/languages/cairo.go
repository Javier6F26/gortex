package languages

import (
	"regexp"
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// Cairo (StarkNet) is Rust-flavoured. The odvcencio Cairo grammar is
// still immature — `#[external]` / `#[view]` attribute-prefixed `fn`
// declarations, trait bodies, and enum variants all drop into ERROR
// recovery, so we can't rely on the AST for faithful symbol extraction.
//
// Strategy: hold the grammar on the struct for parse-probe parity with
// other adapters (enables future upgrade with zero adapter churn), but
// keep the regex-based extractor as the source of truth. When the
// grammar stabilises upstream, swap to a tree-sitter walker.
var (
	cairoFnRe     = regexp.MustCompile(`(?m)^\s*(?:pub\s+)?fn\s+([A-Za-z_]\w*)`)
	cairoStructRe = regexp.MustCompile(`(?m)^\s*(?:pub\s+)?struct\s+([A-Za-z_]\w*)`)
	cairoEnumRe   = regexp.MustCompile(`(?m)^\s*(?:pub\s+)?enum\s+([A-Za-z_]\w*)`)
	cairoTraitRe  = regexp.MustCompile(`(?m)^\s*(?:pub\s+)?trait\s+([A-Za-z_]\w*)`)
	cairoModRe    = regexp.MustCompile(`(?m)^\s*(?:pub\s+)?mod\s+([A-Za-z_]\w*)`)
	cairoUseRe    = regexp.MustCompile(`(?m)^\s*use\s+([\w:]+)`)
)

// CairoExtractor extracts StarkNet Cairo source files. The grammar is
// currently unreliable so the work is regex-based; the `lang` field
// exists for parse-probe parity with the other tree-sitter adapters.
type CairoExtractor struct {
	lang *sitter.Language
}

func NewCairoExtractor() *CairoExtractor {
	return &CairoExtractor{lang: grammars.CairoLanguage()}
}

func (e *CairoExtractor) Language() string     { return "cairo" }
func (e *CairoExtractor) Extensions() []string { return []string{".cairo"} }

func (e *CairoExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	lines := strings.Split(string(src), "\n")
	result := &parser.ExtractionResult{}

	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: len(lines),
		Language: "cairo",
	}
	result.Nodes = append(result.Nodes, fileNode)

	// Parse-probe: ensures the grammar is wired up and the extractor
	// participates in the pure-Go parser lifecycle. Result is ignored
	// because the grammar mis-parses too many Cairo idioms to use.
	if len(src) > 0 {
		if tree, err := parser.ParseFile(src, e.lang); err == nil {
			tree.Close()
		}
	}

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
			Language: "cairo",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	captureBlock := func(re *regexp.Regexp, kind graph.NodeKind) {
		for _, m := range re.FindAllSubmatchIndex(src, -1) {
			name := string(src[m[2]:m[3]])
			line := lineAt(src, m[0])
			add(name, kind, line, findBlockEnd(lines, line))
		}
	}
	captureBlock(cairoFnRe, graph.KindFunction)
	captureBlock(cairoStructRe, graph.KindType)
	captureBlock(cairoEnumRe, graph.KindType)
	captureBlock(cairoTraitRe, graph.KindType)
	captureBlock(cairoModRe, graph.KindType)

	for _, m := range cairoUseRe.FindAllSubmatchIndex(src, -1) {
		mod := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + mod,
			Kind: graph.EdgeImports, FilePath: filePath, Line: line,
		})
	}

	return result, nil
}

var _ parser.Extractor = (*CairoExtractor)(nil)
