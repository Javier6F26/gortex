package languages

import (
	"regexp"
	"strings"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

var (
	erlangModuleRe    = regexp.MustCompile(`(?m)^-module\((\w+)\)`)
	erlangExportRe    = regexp.MustCompile(`(?m)^-export\(\[([^\]]+)\]\)`)
	erlangImportRe    = regexp.MustCompile(`(?m)^-import\((\w+),`)
	erlangBehaviourRe = regexp.MustCompile(`(?m)^-behaviou?r\((\w+)\)`)
	erlangTypeRe      = regexp.MustCompile(`(?m)^-type\s+(\w+)\(`)
	erlangRecordRe    = regexp.MustCompile(`(?m)^-record\((\w+),`)
	erlangSpecRe      = regexp.MustCompile(`(?m)^-spec\s+(\w+)\(`)
	erlangFuncRe      = regexp.MustCompile(`(?m)^(\w+)\(([^)]*)\)\s*->`)
	erlangCallRe      = regexp.MustCompile(`\b(\w+)\s*\(`)
)

// ErlangExtractor extracts Erlang source files using regex.
type ErlangExtractor struct{}

func NewErlangExtractor() *ErlangExtractor { return &ErlangExtractor{} }

func (e *ErlangExtractor) Language() string     { return "erlang" }
func (e *ErlangExtractor) Extensions() []string { return []string{".erl", ".hrl"} }

func (e *ErlangExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	lines := strings.Split(string(src), "\n")
	result := &parser.ExtractionResult{}

	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: len(lines),
		Language: "erlang",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)

	// Module
	if m := erlangModuleRe.FindSubmatchIndex(src); m != nil {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		id := filePath + "::" + name
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindPackage, Name: name,
			FilePath: filePath, StartLine: line, EndLine: line,
			Language: "erlang",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: line,
		})
		seen[id] = true
	}

	// Exports (just record them as metadata, not separate nodes)
	// We track exported names so functions can be marked
	exported := make(map[string]bool)
	for _, m := range erlangExportRe.FindAllSubmatchIndex(src, -1) {
		list := string(src[m[2]:m[3]])
		// Parse "name/arity, name/arity"
		for _, entry := range strings.Split(list, ",") {
			entry = strings.TrimSpace(entry)
			parts := strings.Split(entry, "/")
			if len(parts) >= 1 {
				exported[strings.TrimSpace(parts[0])] = true
			}
		}
	}

	// Imports
	for _, m := range erlangImportRe.FindAllSubmatchIndex(src, -1) {
		mod := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + mod,
			Kind: graph.EdgeImports, FilePath: filePath, Line: line,
		})
	}

	// Behaviours
	for _, m := range erlangBehaviourRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::" + name,
			Kind: graph.EdgeImplements, FilePath: filePath, Line: line,
		})
	}

	// Types
	for _, m := range erlangTypeRe.FindAllSubmatchIndex(src, -1) {
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
			Language: "erlang",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: line,
		})
	}

	// Records
	for _, m := range erlangRecordRe.FindAllSubmatchIndex(src, -1) {
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
			Language: "erlang", Meta: map[string]any{"type_kind": "record"},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: line,
		})
	}

	// Functions: specs first, then definitions
	specNames := make(map[string]bool)
	for _, m := range erlangSpecRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		specNames[name] = true
	}

	// Function definitions
	for _, m := range erlangFuncRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		if isErlangDirective(name) {
			continue
		}
		line := lineAt(src, m[0])
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true

		endLine := erlangFuncEnd(lines, line)
		meta := map[string]any{}
		if exported[name] {
			meta["exported"] = true
		}

		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindFunction, Name: name,
			FilePath: filePath, StartLine: line, EndLine: endLine,
			Language: "erlang", Meta: meta,
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: line,
		})
	}

	// Call sites inside functions
	funcRanges := buildFuncRanges(result)
	for _, m := range erlangCallRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		if isErlangKeyword(name) || isErlangDirective(name) {
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

// erlangFuncEnd finds the end of an Erlang function clause (ends with a period on its own line).
func erlangFuncEnd(lines []string, startLine int) int {
	for i := startLine - 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasSuffix(trimmed, ".") && !strings.HasPrefix(trimmed, "-") {
			return i + 1
		}
	}
	return startLine
}

func isErlangDirective(s string) bool {
	switch s {
	case "module", "export", "import", "behaviour", "behavior",
		"type", "spec", "record", "define", "include", "include_lib",
		"ifdef", "ifndef", "else", "endif", "undef":
		return true
	}
	return false
}

func isErlangKeyword(s string) bool {
	switch s {
	case "if", "case", "of", "end", "fun", "receive", "after",
		"when", "begin", "catch", "try", "throw", "not", "and",
		"or", "band", "bor", "bxor", "bnot", "bsl", "bsr",
		"div", "rem", "let":
		return true
	}
	return false
}

var _ parser.Extractor = (*ErlangExtractor)(nil)
