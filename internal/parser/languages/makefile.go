package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// MakefileExtractor extracts Makefile source using tree-sitter.
//
// Relevant odvcencio grammar nodes:
//   rule                 (targets ":" prerequisites? recipe?)
//     targets              contains one or more `word` children
//   variable_assignment  (word op text)
//   include_directive    ("include" list(word*))
//   define_directive     ("define" word raw_text "endef")
//
// Targets, variables, and `define` blocks become symbol nodes;
// `include` / `-include` / `sinclude` become import edges.
type MakefileExtractor struct {
	lang *sitter.Language
}

func NewMakefileExtractor() *MakefileExtractor {
	return &MakefileExtractor{lang: grammars.MakeLanguage()}
}

func (e *MakefileExtractor) Language() string { return "makefile" }
func (e *MakefileExtractor) Extensions() []string {
	return []string{".mk", ".make", "Makefile", "GNUmakefile", "makefile"}
}

func (e *MakefileExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	tree, err := parser.ParseFile(src, e.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	root := tree.RootNode()
	result := &parser.ExtractionResult{}

	lineCount := strings.Count(string(src), "\n") + 1
	if lineCount < 1 {
		lineCount = 1
	}
	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: lineCount,
		Language: "makefile",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	type topHit struct {
		name string
		line int
		kind graph.NodeKind
	}
	var tops []topHit
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
			Language: "makefile",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	// Iterate direct children of the makefile root in source order so we
	// can compute accurate end-of-range values for rule/variable nodes
	// that have no explicit terminator.
	walkNodes(root, func(n *sitter.Node) {
		if n == root {
			return
		}
		start := int(n.StartPoint().Row) + 1
		end := int(n.EndPoint().Row) + 1

		switch parser.NodeType(n, e.lang) {
		case "rule":
			// One symbol per target `word` in the `targets` child.
			for i := 0; i < int(n.ChildCount()); i++ {
				c := n.Child(i)
				if c == nil || parser.NodeType(c, e.lang) != "targets" {
					continue
				}
				for j := 0; j < int(c.ChildCount()); j++ {
					w := c.Child(j)
					if w == nil || parser.NodeType(w, e.lang) != "word" {
						continue
					}
					name := w.Text(src)
					if isMakeDirective(name) {
						continue
					}
					tops = append(tops, topHit{name: name, line: start, kind: graph.KindFunction})
				}
			}

		case "variable_assignment":
			// LHS is the first `word` child.
			for i := 0; i < int(n.ChildCount()); i++ {
				c := n.Child(i)
				if c != nil && parser.NodeType(c, e.lang) == "word" {
					name := c.Text(src)
					if !isMakeDirective(name) {
						tops = append(tops, topHit{name: name, line: start, kind: graph.KindVariable})
					}
					break
				}
			}

		case "define_directive":
			// Name is the first `word` child; body ends at `endef`.
			for i := 0; i < int(n.ChildCount()); i++ {
				c := n.Child(i)
				if c != nil && parser.NodeType(c, e.lang) == "word" {
					add(c.Text(src), graph.KindFunction, start, end)
					break
				}
			}

		case "include_directive":
			// `include a.mk b.mk` may list several files under `list`.
			for i := 0; i < int(n.ChildCount()); i++ {
				c := n.Child(i)
				if c == nil || parser.NodeType(c, e.lang) != "list" {
					continue
				}
				for j := 0; j < int(c.ChildCount()); j++ {
					w := c.Child(j)
					if w == nil || parser.NodeType(w, e.lang) != "word" {
						continue
					}
					result.Edges = append(result.Edges, &graph.Edge{
						From: fileNode.ID, To: "unresolved::import::" + w.Text(src),
						Kind: graph.EdgeImports, FilePath: filePath, Line: start,
					})
				}
			}
		}
	})

	// Preserve the legacy end-line computation: rule/variable symbols
	// span from their starting line up to the line before the next
	// top-level definition.
	for i := 0; i < len(tops); i++ {
		for j := i + 1; j < len(tops); j++ {
			if tops[j].line < tops[i].line {
				tops[i], tops[j] = tops[j], tops[i]
			}
		}
	}
	for i, t := range tops {
		endLine := lineCount
		if i+1 < len(tops) {
			endLine = tops[i+1].line - 1
			if endLine < t.line {
				endLine = t.line
			}
		}
		add(t.name, t.kind, t.line, endLine)
	}

	return result, nil
}

// isMakeDirective filters out reserved words that `word`-based captures
// would otherwise mistake for identifiers.
func isMakeDirective(s string) bool {
	switch s {
	case "ifeq", "ifneq", "ifdef", "ifndef", "else", "endif",
		"define", "endef", "include", "sinclude", "export",
		"unexport", "override", "vpath", "VPATH":
		return true
	}
	return false
}

var _ parser.Extractor = (*MakefileExtractor)(nil)
