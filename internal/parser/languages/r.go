package languages

import (
	"regexp"
	"strings"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

var (
	rFuncAssignRe = regexp.MustCompile(`(?m)^(\w[\w.]*)\s*<-\s*function\s*\(`)
	rFuncEqRe     = regexp.MustCompile(`(?m)^(\w[\w.]*)\s*=\s*function\s*\(`)
	rVarAssignRe  = regexp.MustCompile(`(?m)^(\w[\w.]*)\s*<-\s*\S`)
	rVarEqRe      = regexp.MustCompile(`(?m)^(\w[\w.]*)\s*=\s*\S`)
	rLibraryRe    = regexp.MustCompile(`(?m)\blibrary\(\s*"?'?(\w+)"?'?\s*\)`)
	rRequireRe    = regexp.MustCompile(`(?m)\brequire\(\s*"?'?(\w+)"?'?\s*\)`)
	rSourceRe     = regexp.MustCompile(`(?m)\bsource\(\s*["']([^"']+)["']\s*\)`)
	rCallRe       = regexp.MustCompile(`\b(\w[\w.]*)\s*\(`)
)

// RExtractor extracts R source files using regex.
type RExtractor struct{}

func NewRExtractor() *RExtractor { return &RExtractor{} }

func (e *RExtractor) Language() string     { return "r" }
func (e *RExtractor) Extensions() []string { return []string{".R", ".r", ".Rmd"} }

func (e *RExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	lines := strings.Split(string(src), "\n")
	result := &parser.ExtractionResult{}

	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: len(lines),
		Language: "r",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)

	// Functions: name <- function( and name = function(
	for _, re := range []*regexp.Regexp{rFuncAssignRe, rFuncEqRe} {
		for _, m := range re.FindAllSubmatchIndex(src, -1) {
			name := string(src[m[2]:m[3]])
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
				Language: "r",
			})
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
				FilePath: filePath, Line: line,
			})
		}
	}

	// Variables: top-level assignments (not function assignments)
	for _, re := range []*regexp.Regexp{rVarAssignRe, rVarEqRe} {
		for _, m := range re.FindAllSubmatchIndex(src, -1) {
			name := string(src[m[2]:m[3]])
			if isRKeyword(name) {
				continue
			}
			line := lineAt(src, m[0])
			id := filePath + "::" + name
			if seen[id] {
				continue
			}
			seen[id] = true
			result.Nodes = append(result.Nodes, &graph.Node{
				ID: id, Kind: graph.KindVariable, Name: name,
				FilePath: filePath, StartLine: line, EndLine: line,
				Language: "r",
			})
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
				FilePath: filePath, Line: line,
			})
		}
	}

	// Imports: library(), require(), source()
	for _, re := range []*regexp.Regexp{rLibraryRe, rRequireRe} {
		for _, m := range re.FindAllSubmatchIndex(src, -1) {
			mod := string(src[m[2]:m[3]])
			line := lineAt(src, m[0])
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: "unresolved::import::" + mod,
				Kind: graph.EdgeImports, FilePath: filePath, Line: line,
			})
		}
	}
	for _, m := range rSourceRe.FindAllSubmatchIndex(src, -1) {
		path := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + path,
			Kind: graph.EdgeImports, FilePath: filePath, Line: line,
		})
	}

	// Call sites inside functions
	funcRanges := buildFuncRanges(result)
	for _, m := range rCallRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		if isRKeyword(name) || name == "function" {
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

func isRKeyword(s string) bool {
	switch s {
	case "if", "else", "for", "while", "repeat", "in", "next", "break",
		"return", "function", "TRUE", "FALSE", "NULL", "NA", "Inf", "NaN",
		"library", "require", "source":
		return true
	}
	return false
}

var _ parser.Extractor = (*RExtractor)(nil)
