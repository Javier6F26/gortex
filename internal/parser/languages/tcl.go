package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// Tcl is a command-dispatch language: everything is a command. The
// high-signal forms are `proc`, `namespace eval`, `package require`,
// and `source`. The odvcencio Tcl grammar surfaces:
//   - `procedure` nodes with `proc` + name as `simple_word`
//   - `namespace` nodes with `eval <name> {...}` inside `word_list`
//   - plain `command` nodes with a leading `simple_word` head
//     ("package", "source") and the rest in a `word_list`.
// Names can be qualified with `::`.

// TclExtractor extracts Tcl source files using tree-sitter.
type TclExtractor struct {
	lang *sitter.Language
}

func NewTclExtractor() *TclExtractor {
	return &TclExtractor{lang: grammars.TclLanguage()}
}

func (e *TclExtractor) Language() string     { return "tcl" }
func (e *TclExtractor) Extensions() []string { return []string{".tcl", ".tk", ".itcl"} }

func (e *TclExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	tree, err := parser.ParseFile(src, e.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	root := tree.RootNode()
	lines := strings.Split(string(src), "\n")
	result := &parser.ExtractionResult{}

	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: len(lines),
		Language: "tcl",
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
			Language: "tcl",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}
	addImport := func(target string, line int) {
		if target == "" {
			return
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + target,
			Kind: graph.EdgeImports, FilePath: filePath, Line: line,
		})
	}

	walkNodes(root, func(node *sitter.Node) {
		switch parser.NodeType(node, e.lang) {
		case "procedure":
			// procedure → proc, simple_word (name), arguments, braced_word
			name := e.procName(node, src)
			start := int(node.StartPoint().Row) + 1
			end := int(node.EndPoint().Row) + 1
			add(name, graph.KindFunction, start, end)
		case "namespace":
			// namespace → namespace keyword, word_list (eval NAME {body})
			name := e.namespaceName(node, src)
			start := int(node.StartPoint().Row) + 1
			end := int(node.EndPoint().Row) + 1
			add(name, graph.KindType, start, end)
		case "command":
			// package require <name> / source <path>
			head := e.headWord(node, src)
			if head == "" {
				return
			}
			line := int(node.StartPoint().Row) + 1
			switch head {
			case "package":
				words := e.commandRest(node, src)
				// `package require Tcl 8.6` → module is words[1]
				if len(words) >= 2 && words[0] == "require" {
					addImport(words[1], line)
				}
			case "source":
				words := e.commandRest(node, src)
				if len(words) >= 1 {
					addImport(words[0], line)
				}
			}
		}
	})

	return result, nil
}

// procName returns the name from a procedure node. The grammar lays it
// out as:
//
//	procedure → proc, simple_word(name), arguments, braced_word
func (e *TclExtractor) procName(node *sitter.Node, src []byte) string {
	seenProc := false
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		t := parser.NodeType(c, e.lang)
		if t == "proc" {
			seenProc = true
			continue
		}
		if seenProc && t == "simple_word" {
			return strings.TrimSpace(c.Text(src))
		}
	}
	return ""
}

// namespaceName extracts the namespace name from
// `namespace eval ::ns {body}`. The grammar nests it inside a
// word_list after the leading `namespace` keyword.
func (e *TclExtractor) namespaceName(node *sitter.Node, src []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, e.lang) != "word_list" {
			continue
		}
		var words []string
		for j := 0; j < int(c.ChildCount()); j++ {
			w := c.Child(j)
			if w == nil {
				continue
			}
			if parser.NodeType(w, e.lang) == "simple_word" {
				words = append(words, strings.TrimSpace(w.Text(src)))
			}
		}
		if len(words) >= 2 && words[0] == "eval" {
			return words[1]
		}
	}
	return ""
}

// headWord returns the text of the first simple_word directly under a
// command node — this is the command's verb.
func (e *TclExtractor) headWord(node *sitter.Node, src []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, e.lang) == "simple_word" {
			return strings.TrimSpace(c.Text(src))
		}
	}
	return ""
}

// commandRest returns the remaining argument words of a command node.
// Arguments live inside a `word_list` sibling of the head `simple_word`.
func (e *TclExtractor) commandRest(node *sitter.Node, src []byte) []string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, e.lang) != "word_list" {
			continue
		}
		var words []string
		for j := 0; j < int(c.ChildCount()); j++ {
			w := c.Child(j)
			if w == nil {
				continue
			}
			switch parser.NodeType(w, e.lang) {
			case "simple_word":
				words = append(words, strings.TrimSpace(w.Text(src)))
			}
		}
		return words
	}
	return nil
}

var _ parser.Extractor = (*TclExtractor)(nil)
