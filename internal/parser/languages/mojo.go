package languages

import (
	"regexp"
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// Mojo is Python-flavored with Rust-like `fn` and `struct`. The pinned
// odvcencio grammar is Python-based and does not model `fn`, `struct`,
// `trait`, `let`, or `var` — it produces ERROR nodes around them. Until
// an upstream Mojo grammar ships we keep regex fallback for every graph
// output. ParseFile still runs (cheap AST skeleton; no-op on error) so
// future migrations only have to flip the switch.
//
// See spec-treesitter-migration.md — Mojo is flagged here explicitly.
var (
	mojoFuncRe   = regexp.MustCompile(`(?m)^\s*(?:async\s+)?(?:fn|def)\s+(\w+)\s*\(`)
	mojoTypeRe   = regexp.MustCompile(`(?m)^\s*(?:struct|trait)\s+(\w+)`)
	mojoFromRe   = regexp.MustCompile(`(?m)^\s*from\s+([\w.]+)\s+import\s+`)
	mojoImportRe = regexp.MustCompile(`(?m)^\s*import\s+([\w.]+)`)
	mojoCallRe   = regexp.MustCompile(`\b([a-zA-Z_]\w*)\s*\(`)
)

// MojoExtractor extracts Mojo source. Tree-sitter skeleton with regex
// fallback for actual extraction (see package comment).
type MojoExtractor struct {
	lang *sitter.Language
}

func NewMojoExtractor() *MojoExtractor {
	return &MojoExtractor{lang: grammars.MojoLanguage()}
}

func (e *MojoExtractor) Language() string     { return "mojo" }
func (e *MojoExtractor) Extensions() []string { return []string{".mojo", ".🔥"} }

func (e *MojoExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	// Best-effort parse; the grammar is known to emit ERROR nodes on
	// valid Mojo. We keep the call for future-proofing and to surface
	// any parser-setup errors (wrong language pointer, etc.).
	if tree, err := parser.ParseFile(src, e.lang); err == nil {
		tree.Close()
	}

	lines := strings.Split(string(src), "\n")
	result := &parser.ExtractionResult{}

	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: len(lines),
		Language: "mojo",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	add := func(name string, kind graph.NodeKind, start, end int) {
		if name == "" || isMojoKeyword(name) {
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
			Language: "mojo",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	for _, m := range mojoFuncRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		add(name, graph.KindFunction, line, findIndentedBlockEnd(lines, line))
	}
	for _, m := range mojoTypeRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		add(name, graph.KindType, line, findIndentedBlockEnd(lines, line))
	}

	for _, m := range mojoFromRe.FindAllSubmatchIndex(src, -1) {
		mod := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + mod,
			Kind: graph.EdgeImports, FilePath: filePath, Line: line,
		})
	}
	for _, m := range mojoImportRe.FindAllSubmatchIndex(src, -1) {
		mod := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + mod,
			Kind: graph.EdgeImports, FilePath: filePath, Line: line,
		})
	}

	funcRanges := buildFuncRanges(result)
	for _, m := range mojoCallRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		if isMojoKeyword(name) {
			continue
		}
		line := lineAt(src, m[0])
		callerID := findEnclosingFunc(funcRanges, line)
		if callerID == "" || strings.HasSuffix(callerID, "::"+name) {
			continue
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: callerID, To: "unresolved::" + name,
			Kind: graph.EdgeCalls, FilePath: filePath, Line: line,
		})
	}

	return result, nil
}

func isMojoKeyword(s string) bool {
	switch s {
	case "if", "elif", "else", "while", "for", "in", "not", "and", "or",
		"fn", "def", "struct", "trait", "alias", "var", "let",
		"return", "yield", "raise", "try", "except", "finally",
		"import", "from", "as", "with", "pass", "break", "continue",
		"True", "False", "None", "self", "async", "await", "owned",
		"borrowed", "inout", "mut", "ref":
		return true
	}
	return false
}

var _ parser.Extractor = (*MojoExtractor)(nil)
