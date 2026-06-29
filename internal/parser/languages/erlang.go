package languages

import (
	"regexp"
	"strings"

	erlangforest "github.com/alexaandru/go-sitter-forest/erlang"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
	"github.com/zzet/gortex/internal/parser/forest"
)

// Erlang migration: forest's walker (with the per-language
// `fun_decl` mapping) catches function declarations. Erlang's
// attribute idioms — `-module`, `-export`, `-import`, `-behaviour`,
// `-type`, `-record` — don't fit any tags.scm convention and need
// bespoke graph mapping (behaviours → EdgeImplements, exports →
// Meta["exported"], records → KindType + Meta["type_kind"]
// = "record"). Those stay regex.
var (
	erlangModuleRe    = regexp.MustCompile(`(?m)^-module\((\w+)\)`)
	erlangExportRe    = regexp.MustCompile(`(?m)^-export\(\[([^\]]+)\]\)`)
	erlangImportRe    = regexp.MustCompile(`(?m)^-import\((\w+),`)
	erlangBehaviourRe = regexp.MustCompile(`(?m)^-behaviou?r\((\w+)\)`)
	erlangTypeRe      = regexp.MustCompile(`(?m)^-type\s+(\w+)\(`)
	erlangRecordRe    = regexp.MustCompile(`(?m)^-record\((\w+),`)
)

type ErlangExtractor struct {
	forest *forest.Extractor
}

func NewErlangExtractor() *ErlangExtractor {
	return &ErlangExtractor{
		forest: forest.New("erlang", []string{".erl", ".hrl"}, erlangforest.GetLanguage, erlangforest.GetQuery),
	}
}

func (e *ErlangExtractor) Language() string     { return "erlang" }
func (e *ErlangExtractor) Extensions() []string { return []string{".erl", ".hrl"} }

func (e *ErlangExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	res, err := e.forest.Extract(filePath, src)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	for _, n := range res.Nodes {
		seen[n.ID] = true
	}

	if m := erlangModuleRe.FindSubmatchIndex(src); m != nil {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		id := filePath + "::" + name
		if !seen[id] {
			seen[id] = true
			res.Nodes = append(res.Nodes, &graph.Node{
				ID: id, Kind: graph.KindPackage, Name: name,
				FilePath: filePath, StartLine: line, EndLine: line,
				Language: "erlang", Meta: map[string]any{"type_flavor": "module"},
			})
			res.Edges = append(res.Edges, &graph.Edge{
				From: filePath, To: id, Kind: graph.EdgeDefines,
				FilePath: filePath, Line: line,
			})
		}
	}

	exported := make(map[string]bool)
	for _, m := range erlangExportRe.FindAllSubmatchIndex(src, -1) {
		list := string(src[m[2]:m[3]])
		for _, entry := range strings.Split(list, ",") {
			parts := strings.SplitN(strings.TrimSpace(entry), "/", 2)
			if parts[0] != "" {
				exported[strings.TrimSpace(parts[0])] = true
			}
		}
	}
	for _, n := range res.Nodes {
		if n.Kind == graph.KindFunction && exported[n.Name] {
			if n.Meta == nil {
				n.Meta = map[string]any{}
			}
			n.Meta["exported"] = true
		}
	}

	for _, m := range erlangImportRe.FindAllSubmatchIndex(src, -1) {
		mod := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		res.Edges = append(res.Edges, &graph.Edge{
			From: filePath, To: "unresolved::import::" + mod,
			Kind: graph.EdgeImports, FilePath: filePath, Line: line,
		})
	}

	for _, m := range erlangBehaviourRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		res.Edges = append(res.Edges, &graph.Edge{
			From: filePath, To: "unresolved::" + name,
			Kind: graph.EdgeImplements, FilePath: filePath, Line: line,
		})
	}

	for _, m := range erlangTypeRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		res.Nodes = append(res.Nodes, &graph.Node{
			ID: id, Kind: graph.KindType, Name: name,
			FilePath: filePath, StartLine: line, EndLine: line,
			Language: "erlang",
		})
		res.Edges = append(res.Edges, &graph.Edge{
			From: filePath, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: line,
		})
	}
	for _, m := range erlangRecordRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		res.Nodes = append(res.Nodes, &graph.Node{
			ID: id, Kind: graph.KindType, Name: name,
			FilePath: filePath, StartLine: line, EndLine: line,
			Language: "erlang", Meta: map[string]any{"type_kind": "record"},
		})
		res.Edges = append(res.Edges, &graph.Edge{
			From: filePath, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: line,
		})
	}

	return res, nil
}

var _ parser.Extractor = (*ErlangExtractor)(nil)
