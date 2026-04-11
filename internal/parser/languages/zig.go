package languages

import (
	"regexp"
	"strings"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

var (
	zigFuncRe   = regexp.MustCompile(`(?m)^[ \t]*(pub\s+)?fn\s+(\w+)\s*\(`)
	zigStructRe = regexp.MustCompile(`(?m)^[ \t]*(pub\s+)?const\s+(\w+)\s*=\s*(struct|enum|union)\s*\{`)
	zigImportRe = regexp.MustCompile(`@import\("([^"]+)"\)`)
	zigConstRe  = regexp.MustCompile(`(?m)^[ \t]*(pub\s+)?const\s+(\w+)\s*=\s*\S`)
	zigVarRe    = regexp.MustCompile(`(?m)^[ \t]*(pub\s+)?var\s+(\w+)\s*`)
	zigCallRe   = regexp.MustCompile(`\b([a-zA-Z_]\w*)\s*\(`)
)

// ZigExtractor extracts Zig source files using regex.
type ZigExtractor struct{}

func NewZigExtractor() *ZigExtractor { return &ZigExtractor{} }

func (e *ZigExtractor) Language() string     { return "zig" }
func (e *ZigExtractor) Extensions() []string { return []string{".zig"} }

func (e *ZigExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	text := string(src)
	lines := strings.Split(text, "\n")
	result := &parser.ExtractionResult{}

	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: len(lines),
		Language: "zig",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)

	// Functions
	for _, m := range zigFuncRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[4]:m[5]])
		line := lineAt(src, m[0])
		endLine := findBlockEnd(lines, line)
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindFunction, Name: name,
			FilePath: filePath, StartLine: line, EndLine: endLine,
			Language: "zig",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: line,
		})
	}

	// Types (struct, enum, union)
	for _, m := range zigStructRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[4]:m[5]])
		kind := string(src[m[6]:m[7]])
		line := lineAt(src, m[0])
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindType, Name: name,
			FilePath: filePath, StartLine: line, EndLine: line,
			Language: "zig", Meta: map[string]any{"type_kind": kind},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: line,
		})
	}

	// Imports
	for _, m := range zigImportRe.FindAllSubmatchIndex(src, -1) {
		mod := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + mod,
			Kind: graph.EdgeImports, FilePath: filePath, Line: line,
		})
	}

	// Variables (const that are not struct/enum/union, and var)
	for _, m := range zigConstRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[4]:m[5]])
		line := lineAt(src, m[0])
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindVariable, Name: name,
			FilePath: filePath, StartLine: line, EndLine: line,
			Language: "zig",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: line,
		})
	}
	for _, m := range zigVarRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[4]:m[5]])
		line := lineAt(src, m[0])
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindVariable, Name: name,
			FilePath: filePath, StartLine: line, EndLine: line,
			Language: "zig",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: line,
		})
	}

	// Call sites inside functions
	funcRanges := buildFuncRanges(result)
	for _, m := range zigCallRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		if name == "fn" || name == "pub" || name == "const" || name == "var" || name == "if" || name == "while" || name == "for" || name == "switch" || name == "return" {
			continue
		}
		line := lineAt(src, m[0])
		callerID := findEnclosingFunc(funcRanges, line)
		if callerID == "" {
			continue
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: callerID, To: "unresolved::" + name,
			Kind: graph.EdgeCalls, FilePath: filePath, Line: line,
		})
	}

	return result, nil
}

// lineAt returns the 1-based line number for byte offset pos.
func lineAt(src []byte, pos int) int {
	line := 1
	for i := 0; i < pos && i < len(src); i++ {
		if src[i] == '\n' {
			line++
		}
	}
	return line
}

// findBlockEnd finds the approximate end line of a brace-delimited block starting at startLine (1-based).
func findBlockEnd(lines []string, startLine int) int {
	depth := 0
	for i := startLine - 1; i < len(lines); i++ {
		for _, ch := range lines[i] {
			switch ch {
			case '{':
				depth++
			case '}':
				depth--
				if depth <= 0 {
					return i + 1
				}
			}
		}
	}
	return startLine
}

var _ parser.Extractor = (*ZigExtractor)(nil)
