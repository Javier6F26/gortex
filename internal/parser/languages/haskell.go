package languages

import (
	"regexp"
	"strings"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

var (
	haskellModuleRe    = regexp.MustCompile(`(?m)^module\s+([\w.]+)`)
	haskellImportRe    = regexp.MustCompile(`(?m)^import\s+(?:qualified\s+)?([\w.]+)`)
	haskellDataRe      = regexp.MustCompile(`(?m)^data\s+(\w+)`)
	haskellNewtypeRe   = regexp.MustCompile(`(?m)^newtype\s+(\w+)`)
	haskellTypeAliasRe = regexp.MustCompile(`(?m)^type\s+(\w+)`)
	haskellClassRe     = regexp.MustCompile(`(?m)^class\s+(?:.*=>\s*)?(\w+)`)
	haskellInstanceRe  = regexp.MustCompile(`(?m)^instance\s+(?:.*=>\s*)?(\w+)`)
	haskellTypeSigRe   = regexp.MustCompile(`(?m)^(\w+)\s*::`)
	haskellFuncDefRe   = regexp.MustCompile(`(?m)^(\w+)\s+[^:=\n].*=`)
	haskellCallRe      = regexp.MustCompile(`\b([a-z_]\w*)\b`)
)

// HaskellExtractor extracts Haskell source files using regex.
type HaskellExtractor struct{}

func NewHaskellExtractor() *HaskellExtractor { return &HaskellExtractor{} }

func (e *HaskellExtractor) Language() string     { return "haskell" }
func (e *HaskellExtractor) Extensions() []string { return []string{".hs", ".lhs"} }

func (e *HaskellExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	lines := strings.Split(string(src), "\n")
	result := &parser.ExtractionResult{}

	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: len(lines),
		Language: "haskell",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)

	// Module declaration
	if m := haskellModuleRe.FindSubmatch(src); m != nil {
		name := string(m[1])
		id := filePath + "::" + name
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindPackage, Name: name,
			FilePath: filePath, StartLine: 1, EndLine: 1,
			Language: "haskell",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: 1,
		})
		seen[id] = true
	}

	// Imports
	for _, m := range haskellImportRe.FindAllSubmatchIndex(src, -1) {
		mod := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + mod,
			Kind: graph.EdgeImports, FilePath: filePath, Line: line,
		})
	}

	// data types
	for _, m := range haskellDataRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindType, Name: name,
			FilePath: filePath, StartLine: line, EndLine: line,
			Language: "haskell", Meta: map[string]any{"type_kind": "data"},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: line,
		})
	}

	// newtype
	for _, m := range haskellNewtypeRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindType, Name: name,
			FilePath: filePath, StartLine: line, EndLine: line,
			Language: "haskell", Meta: map[string]any{"type_kind": "newtype"},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: line,
		})
	}

	// type alias
	for _, m := range haskellTypeAliasRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindType, Name: name,
			FilePath: filePath, StartLine: line, EndLine: line,
			Language: "haskell", Meta: map[string]any{"type_kind": "type"},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: line,
		})
	}

	// class
	for _, m := range haskellClassRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindInterface, Name: name,
			FilePath: filePath, StartLine: line, EndLine: line,
			Language: "haskell",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: line,
		})
	}

	// instance
	for _, m := range haskellInstanceRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		id := filePath + "::instance:" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindType, Name: name + " instance",
			FilePath: filePath, StartLine: line, EndLine: line,
			Language: "haskell", Meta: map[string]any{"type_kind": "instance"},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: line,
		})
		// implements edge
		result.Edges = append(result.Edges, &graph.Edge{
			From: id, To: "unresolved::" + name,
			Kind: graph.EdgeImplements, FilePath: filePath, Line: line,
		})
	}

	// Functions: type signatures then definitions
	// Collect type signatures as functions
	funcNames := make(map[string]bool)
	for _, m := range haskellTypeSigRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		if isHaskellKeyword(name) {
			continue
		}
		line := lineAt(src, m[0])
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		funcNames[name] = true

		// Try to find function end (next top-level definition)
		endLine := haskellFuncEnd(lines, line)
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindFunction, Name: name,
			FilePath: filePath, StartLine: line, EndLine: endLine,
			Language: "haskell",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: line,
		})
	}

	// Definitions without type signatures
	for _, m := range haskellFuncDefRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		if isHaskellKeyword(name) || funcNames[name] {
			continue
		}
		line := lineAt(src, m[0])
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true

		endLine := haskellFuncEnd(lines, line)
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindFunction, Name: name,
			FilePath: filePath, StartLine: line, EndLine: endLine,
			Language: "haskell",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: line,
		})
	}

	// Call sites inside functions
	funcRanges := buildFuncRanges(result)
	for _, m := range haskellCallRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		if isHaskellKeyword(name) || len(name) < 2 {
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

func isHaskellKeyword(s string) bool {
	switch s {
	case "module", "where", "import", "qualified", "as", "hiding",
		"data", "newtype", "type", "class", "instance", "deriving",
		"if", "then", "else", "case", "of", "let", "in", "do",
		"return", "forall", "infixl", "infixr", "infix",
		"main":
		return true
	}
	return false
}

func haskellFuncEnd(lines []string, startLine int) int {
	// A Haskell function ends at the line before the next top-level definition
	// (line that starts with a non-space character and is not a continuation).
	for i := startLine; i < len(lines); i++ { // startLine is 1-based, lines is 0-based
		l := lines[i]
		if len(l) == 0 {
			continue
		}
		if l[0] != ' ' && l[0] != '\t' && i > startLine {
			return i // line before this is end
		}
	}
	return len(lines)
}

var _ parser.Extractor = (*HaskellExtractor)(nil)
