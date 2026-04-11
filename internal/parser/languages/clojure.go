package languages

import (
	"regexp"
	"strings"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

var (
	clojureNsRe      = regexp.MustCompile(`(?m)\(ns\s+([\w.\-]+)`)
	clojureDefnRe    = regexp.MustCompile(`(?m)\(defn-?\s+([\w\-!?*+<>=]+)`)
	clojureMacroRe   = regexp.MustCompile(`(?m)\(defmacro\s+([\w\-!?*+<>=]+)`)
	clojureRecordRe  = regexp.MustCompile(`(?m)\(defrecord\s+(\w+)`)
	clojureTypeRe    = regexp.MustCompile(`(?m)\(deftype\s+(\w+)`)
	clojureProtoRe   = regexp.MustCompile(`(?m)\(defprotocol\s+(\w+)`)
	clojureRequireRe = regexp.MustCompile(`(?m)(?:\(require|\(:require|\(use|\(:import)\s+[\[\s]*(?:\[?\s*)?(['\s]?[\w.\-]+)`)
	clojureCallRe    = regexp.MustCompile(`\(([\w\-!?*+<>=]+)[\s)]`)
)

// ClojureExtractor extracts Clojure source files using regex.
type ClojureExtractor struct{}

func NewClojureExtractor() *ClojureExtractor { return &ClojureExtractor{} }

func (e *ClojureExtractor) Language() string     { return "clojure" }
func (e *ClojureExtractor) Extensions() []string { return []string{".clj", ".cljs", ".cljc", ".edn"} }

func (e *ClojureExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	lines := strings.Split(string(src), "\n")
	result := &parser.ExtractionResult{}

	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: len(lines),
		Language: "clojure",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)

	// Namespace
	if m := clojureNsRe.FindSubmatchIndex(src); m != nil {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		id := filePath + "::" + name
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindPackage, Name: name,
			FilePath: filePath, StartLine: line, EndLine: line,
			Language: "clojure",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: line,
		})
		seen[id] = true
	}

	// Functions (defn, defn-)
	for _, m := range clojureDefnRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		endLine := clojureFormEnd(lines, line)
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindFunction, Name: name,
			FilePath: filePath, StartLine: line, EndLine: endLine,
			Language: "clojure",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: line,
		})
	}

	// Macros
	for _, m := range clojureMacroRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindFunction, Name: name,
			FilePath: filePath, StartLine: line, EndLine: line,
			Language: "clojure", Meta: map[string]any{"macro": true},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: line,
		})
	}

	// Types: defrecord, deftype, defprotocol
	for _, re := range []*regexp.Regexp{clojureRecordRe, clojureTypeRe, clojureProtoRe} {
		for _, m := range re.FindAllSubmatchIndex(src, -1) {
			name := string(src[m[2]:m[3]])
			line := lineAt(src, m[0])
			kind := graph.KindType
			if re == clojureProtoRe {
				kind = graph.KindInterface
			}
			id := filePath + "::" + name
			if seen[id] {
				continue
			}
			seen[id] = true
			result.Nodes = append(result.Nodes, &graph.Node{
				ID: id, Kind: kind, Name: name,
				FilePath: filePath, StartLine: line, EndLine: line,
				Language: "clojure",
			})
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
				FilePath: filePath, Line: line,
			})
		}
	}

	// Variables (def, excluding defn/defmacro/defrecord/deftype/defprotocol)
	defAllRe := regexp.MustCompile(`(?m)\(def\s+([\w\-!?*+<>=]+)`)
	for _, m := range defAllRe.FindAllSubmatchIndex(src, -1) {
		// Check that "def" is not followed by n, macro, record, type, protocol
		afterDef := string(src[m[0]+4 : m[2]])
		if strings.HasPrefix(strings.TrimSpace(afterDef), "n") ||
			strings.HasPrefix(strings.TrimSpace(afterDef), "macro") ||
			strings.HasPrefix(strings.TrimSpace(afterDef), "record") ||
			strings.HasPrefix(strings.TrimSpace(afterDef), "type") ||
			strings.HasPrefix(strings.TrimSpace(afterDef), "protocol") {
			continue
		}
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindVariable, Name: name,
			FilePath: filePath, StartLine: line, EndLine: line,
			Language: "clojure",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: line,
		})
	}

	// Imports
	for _, m := range clojureRequireRe.FindAllSubmatchIndex(src, -1) {
		mod := strings.TrimLeft(string(src[m[2]:m[3]]), "' ")
		if mod == "" {
			continue
		}
		line := lineAt(src, m[0])
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + mod,
			Kind: graph.EdgeImports, FilePath: filePath, Line: line,
		})
	}

	// Call sites inside functions
	funcRanges := buildFuncRanges(result)
	for _, m := range clojureCallRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		if isClojureSpecialForm(name) {
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

// clojureFormEnd finds the end of a top-level form by matching parens.
func clojureFormEnd(lines []string, startLine int) int {
	depth := 0
	for i := startLine - 1; i < len(lines); i++ {
		for _, ch := range lines[i] {
			switch ch {
			case '(':
				depth++
			case ')':
				depth--
				if depth <= 0 {
					return i + 1
				}
			}
		}
	}
	return startLine
}

func isClojureSpecialForm(s string) bool {
	switch s {
	case "if", "do", "let", "fn", "def", "defn", "defn-", "defmacro",
		"defrecord", "deftype", "defprotocol", "ns", "require", "use",
		"import", "quote", "loop", "recur", "throw", "try", "catch",
		"finally", "cond", "case", "when", "when-not", "when-let",
		"if-let", "for", "doseq", "dotimes":
		return true
	}
	return false
}

var _ parser.Extractor = (*ClojureExtractor)(nil)
