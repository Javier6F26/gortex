package languages

import (
	"regexp"

	haskellforest "github.com/alexaandru/go-sitter-forest/haskell"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
	"github.com/zzet/gortex/internal/parser/forest"
)

// Haskell migration: forest's walker (with per-language map for
// `function` / `signature` / `data_type` / `class` / `instance`)
// covers the structural shape. Module declarations and `import`
// edges are idiom-specific and stay as regex; instance →
// EdgeImplements pairs the forest-emitted instance node with the
// implemented class.
var (
	haskellModuleRe   = regexp.MustCompile(`(?m)^module\s+([\w.]+)`)
	haskellImportRe   = regexp.MustCompile(`(?m)^import\s+(?:qualified\s+)?([\w.]+)`)
	haskellInstanceRe = regexp.MustCompile(`(?m)^instance\s+(?:.*=>\s*)?(\w+)`)
)

type HaskellExtractor struct {
	forest *forest.Extractor
}

func NewHaskellExtractor() *HaskellExtractor {
	return &HaskellExtractor{
		forest: forest.New("haskell", []string{".hs", ".lhs"}, haskellforest.GetLanguage, haskellforest.GetQuery),
	}
}

func (e *HaskellExtractor) Language() string     { return "haskell" }
func (e *HaskellExtractor) Extensions() []string { return []string{".hs", ".lhs"} }

func (e *HaskellExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	res, err := e.forest.Extract(filePath, src)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	for _, n := range res.Nodes {
		seen[n.ID] = true
	}

	if m := haskellModuleRe.FindSubmatchIndex(src); m != nil {
		name := string(src[m[2]:m[3]])
		id := filePath + "::" + name
		if !seen[id] {
			seen[id] = true
			res.Nodes = append(res.Nodes, &graph.Node{
				ID: id, Kind: graph.KindPackage, Name: name,
				FilePath: filePath, StartLine: 1, EndLine: 1,
				Language: "haskell",
			})
			res.Edges = append(res.Edges, &graph.Edge{
				From: filePath, To: id, Kind: graph.EdgeDefines,
				FilePath: filePath, Line: 1,
			})
		}
	}

	for _, m := range haskellImportRe.FindAllSubmatchIndex(src, -1) {
		mod := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		res.Edges = append(res.Edges, &graph.Edge{
			From: filePath, To: "unresolved::import::" + mod,
			Kind: graph.EdgeImports, FilePath: filePath, Line: line,
		})
	}

	for _, m := range haskellInstanceRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		instanceID := filePath + "::instance:" + name
		if !seen[instanceID] {
			seen[instanceID] = true
			res.Nodes = append(res.Nodes, &graph.Node{
				ID: instanceID, Kind: graph.KindType, Name: name + " instance",
				FilePath: filePath, StartLine: line, EndLine: line,
				Language: "haskell", Meta: map[string]any{"type_kind": "instance", "type_flavor": "instance"},
			})
			res.Edges = append(res.Edges, &graph.Edge{
				From: filePath, To: instanceID, Kind: graph.EdgeDefines,
				FilePath: filePath, Line: line,
			})
		}
		res.Edges = append(res.Edges, &graph.Edge{
			From: instanceID, To: "unresolved::" + name,
			Kind: graph.EdgeImplements, FilePath: filePath, Line: line,
		})
	}

	return res, nil
}

var _ parser.Extractor = (*HaskellExtractor)(nil)
