package languages

import (
	"regexp"
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// Move (Sui / Aptos) is brace-delimited and Rust-flavoured. The pinned
// odvcencio Move grammar does not parse either Aptos-flavoured Move
// (`module demo::coin { … struct Vault has key { … } … public fun …}`)
// nor script blocks — it produces ERROR nodes around most constructs.
// We keep regex fallback for every graph output until the grammar is
// upgraded. ParseFile still runs so future migrations only have to flip
// the switch.
//
// See spec-treesitter-migration.md — Move is flagged here explicitly.
var (
	moveModuleRe = regexp.MustCompile(`(?m)^\s*module\s+(?:[\w]+::)?([A-Za-z_]\w*)\s*\{`)
	moveFunRe    = regexp.MustCompile(`(?m)^\s*(?:public(?:\([a-z]+\))?\s+)?(?:entry\s+)?fun\s+([A-Za-z_]\w*)`)
	moveStructRe = regexp.MustCompile(`(?m)^\s*(?:public\s+)?struct\s+([A-Za-z_]\w*)`)
	moveUseRe    = regexp.MustCompile(`(?m)^\s*use\s+([\w:]+)`)
)

// MoveExtractor extracts Move (Sui / Aptos) source. Tree-sitter skeleton
// with regex fallback for actual extraction (see package comment).
type MoveExtractor struct {
	lang *sitter.Language
}

func NewMoveExtractor() *MoveExtractor {
	return &MoveExtractor{lang: grammars.MoveLanguage()}
}

func (e *MoveExtractor) Language() string     { return "move" }
func (e *MoveExtractor) Extensions() []string { return []string{".move"} }

func (e *MoveExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	// Best-effort parse; the grammar emits ERROR nodes on valid Move.
	if tree, err := parser.ParseFile(src, e.lang); err == nil {
		tree.Close()
	}

	lines := strings.Split(string(src), "\n")
	result := &parser.ExtractionResult{}

	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: len(lines),
		Language: "move",
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
			Language: "move",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	for _, m := range moveModuleRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		add(name, graph.KindType, line, findBlockEnd(lines, line))
	}
	for _, m := range moveFunRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		add(name, graph.KindFunction, line, findBlockEnd(lines, line))
	}
	for _, m := range moveStructRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		add(name, graph.KindType, line, findBlockEnd(lines, line))
	}

	for _, m := range moveUseRe.FindAllSubmatchIndex(src, -1) {
		mod := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + mod,
			Kind: graph.EdgeImports, FilePath: filePath, Line: line,
		})
	}

	return result, nil
}

var _ parser.Extractor = (*MoveExtractor)(nil)
