package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// Blade is Laravel's templating engine. Directives start with `@` and
// their arguments are parenthesised string literals. The extractor
// models `@section`, `@yield`, `@component`, `@include` as function
// nodes, and `@extends` as an import edge so cross-template inheritance
// shows up in the graph.
//
// The odvcencio Blade grammar emits:
//   - bare directives as a flat sequence: `directive` node ("@include")
//     followed by a sibling `parameter` node ("'partials.nav'").
//   - block-form directives as a wrapper node (`section` / `conditional`
//     / etc.) containing a `directive_start` + `parameter` + body +
//     `directive_end`.
//
// We walk the whole tree looking for (directive_or_directive_start, next
// `parameter` sibling) pairs and pull the first quoted string out of the
// parameter text.
type BladeExtractor struct {
	lang *sitter.Language
}

func NewBladeExtractor() *BladeExtractor {
	return &BladeExtractor{lang: grammars.BladeLanguage()}
}

func (e *BladeExtractor) Language() string     { return "blade" }
func (e *BladeExtractor) Extensions() []string { return []string{".blade", ".blade.php"} }

func (e *BladeExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	lines := strings.Split(string(src), "\n")
	result := &parser.ExtractionResult{}

	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: len(lines),
		Language: "blade",
	}
	result.Nodes = append(result.Nodes, fileNode)

	if len(src) == 0 {
		return result, nil
	}

	tree, err := parser.ParseFile(src, e.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()
	root := tree.RootNode()

	seen := make(map[string]bool)
	addDef := func(name string, line int) {
		if name == "" {
			return
		}
		id := filePath + "::" + name
		if seen[id] {
			return
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindFunction, Name: name,
			FilePath: filePath, StartLine: line, EndLine: line,
			Language: "blade",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: line,
		})
	}

	// Walk children at each level, pairing a directive (`@name`) with
	// its immediately following `parameter` sibling. Block directives
	// get the same treatment at their `directive_start` child.
	var visit func(n *sitter.Node)
	visit = func(n *sitter.Node) {
		if n == nil {
			return
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			if c == nil {
				continue
			}
			ct := parser.NodeType(c, e.lang)
			switch ct {
			case "directive", "directive_start":
				// Directive text includes the leading '@' (e.g. "@section");
				// TrimPrefix strips it or no-ops when absent.
				name := strings.TrimPrefix(strings.TrimSpace(c.Text(src)), "@")
				// Find the next sibling `parameter` within this parent.
				var param *sitter.Node
				for j := i + 1; j < int(n.ChildCount()); j++ {
					sib := n.Child(j)
					if sib == nil {
						continue
					}
					if parser.NodeType(sib, e.lang) == "parameter" {
						param = sib
						break
					}
					// parameter nodes should be adjacent; bail out
					// if we hit anything substantive first.
					if sib.IsNamed() {
						break
					}
				}
				if param == nil {
					continue
				}
				arg := firstBladeString(param.Text(src))
				if arg == "" {
					continue
				}
				line := int(c.StartPoint().Row) + 1
				switch name {
				case "extends":
					result.Edges = append(result.Edges, &graph.Edge{
						From: fileNode.ID, To: "unresolved::import::" + arg,
						Kind: graph.EdgeImports, FilePath: filePath, Line: line,
					})
				case "section", "yield", "component", "include":
					addDef(arg, line)
				}
			}
			// Recurse into block wrappers (`section`, `conditional`, …).
			visit(c)
		}
	}
	visit(root)

	return result, nil
}

// firstBladeString extracts the first single- or double-quoted token out
// of a parameter's raw text (which still carries the surrounding
// parentheses and any trailing args like `@section('content', …)`).
func firstBladeString(s string) string {
	// Find first ' or ".
	qi := strings.IndexAny(s, `"'`)
	if qi < 0 {
		return ""
	}
	quote := s[qi]
	rest := s[qi+1:]
	end := strings.IndexByte(rest, quote)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

var _ parser.Extractor = (*BladeExtractor)(nil)
