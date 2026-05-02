package languages

import (
	"strings"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// annotationArgsMaxLen caps the verbatim argument text stored on the
// edge. 200 chars covers route strings, dependency-injection tokens,
// and most decorator literals without bloating large response
// payloads.
const annotationArgsMaxLen = 200

// AnnotationNodeID returns the canonical synthetic annotation node ID
// for a (language, name) pair. The same ID is reused across every
// site that applies the annotation, so `find_usages` on the node
// returns every annotated symbol in one hop. Keep this in sync with
// any consumer that constructs annotation IDs by hand.
func AnnotationNodeID(lang, name string) string {
	return "annotation::" + lang + "::" + name
}

// EmitAnnotationEdge appends a synthetic annotation node (idempotent
// per ID) plus an EdgeAnnotated edge from `fromID` to that node.
// `name` is the annotation's bare name (no leading `@`, `#[`, or `[`).
// `args` is the raw inner-parentheses text (e.g. `"/users/:id"` for
// `@Get("/users/:id")`) — pass "" when the annotation has no args.
//
// `seen` deduplicates the synthetic node creation within the current
// extraction pass so multiple files / multiple decorator sites in the
// same file don't each emit a duplicate annotation node. The graph
// layer also dedupes by ID, so missing dedup here is a wasted append
// rather than a correctness bug.
func EmitAnnotationEdge(
	fromID, lang, name, args, filePath string,
	line int,
	result *parser.ExtractionResult,
	seen map[string]bool,
) {
	if name == "" || fromID == "" {
		return
	}
	annoID := AnnotationNodeID(lang, name)
	if !seen[annoID] {
		seen[annoID] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID:        annoID,
			Kind:      graph.KindType,
			Name:      name,
			FilePath:  filePath,
			StartLine: line,
			EndLine:   line,
			Language:  lang,
			Meta: map[string]any{
				"kind":     "annotation",
				"synthetic": true,
			},
		})
	}
	edge := &graph.Edge{
		From:     fromID,
		To:       annoID,
		Kind:     graph.EdgeAnnotated,
		FilePath: filePath,
		Line:     line,
		Origin:   graph.OriginASTResolved,
	}
	if trimmed := truncateAnnotationArgs(args); trimmed != "" {
		edge.Meta = map[string]any{"args": trimmed}
	}
	result.Edges = append(result.Edges, edge)
}

// truncateAnnotationArgs trims surrounding whitespace and caps the
// argument text to annotationArgsMaxLen on a rune boundary. Returns
// "" for empty / pure-whitespace input.
func truncateAnnotationArgs(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) <= annotationArgsMaxLen {
		return s
	}
	cut := annotationArgsMaxLen
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

// ExtractParenArgs returns the substring between the outermost balanced
// parens in `s`, or "" when there are none. The result excludes the
// parens themselves. Useful when an extractor has the verbatim source
// of a decorator call like `@Get("/users")` and wants just the args.
func ExtractParenArgs(s string) string {
	open := strings.IndexByte(s, '(')
	if open < 0 {
		return ""
	}
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[open+1 : i]
			}
		}
	}
	return ""
}
