package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// GleamExtractor extracts Gleam source using tree-sitter.
type GleamExtractor struct {
	lang *sitter.Language
}

func NewGleamExtractor() *GleamExtractor {
	return &GleamExtractor{lang: grammars.GleamLanguage()}
}

func (e *GleamExtractor) Language() string     { return "gleam" }
func (e *GleamExtractor) Extensions() []string { return []string{".gleam"} }

func (e *GleamExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	tree, err := parser.ParseFile(src, e.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	root := tree.RootNode()
	result := &parser.ExtractionResult{}

	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: int(root.EndPoint().Row) + 1,
		Language: "gleam",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	add := func(name string, kind graph.NodeKind, start, end int) {
		if name == "" || isGleamKeyword(name) {
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
			Language: "gleam",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	// Top-level declarations are direct children of source_file.
	for i := 0; i < int(root.NamedChildCount()); i++ {
		n := root.NamedChild(i)
		t := parser.NodeType(n, e.lang)
		switch t {
		case "import":
			// import → module (text is slash-separated path).
			mod := ""
			for j := 0; j < int(n.NamedChildCount()); j++ {
				c := n.NamedChild(j)
				if parser.NodeType(c, e.lang) == "module" {
					mod = c.Text(src)
					break
				}
			}
			if mod == "" {
				continue
			}
			line := int(n.StartPoint().Row) + 1
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: "unresolved::import::" + mod,
				Kind: graph.EdgeImports, FilePath: filePath, Line: line,
			})

		case "type_definition":
			// type_definition → type_name → type_identifier.
			name := ""
			for j := 0; j < int(n.NamedChildCount()); j++ {
				c := n.NamedChild(j)
				if parser.NodeType(c, e.lang) == "type_name" {
					for k := 0; k < int(c.NamedChildCount()); k++ {
						g := c.NamedChild(k)
						if parser.NodeType(g, e.lang) == "type_identifier" {
							name = g.Text(src)
							break
						}
					}
					break
				}
			}
			add(name, graph.KindType,
				int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1)

		case "function":
			// function → identifier (first direct named child).
			name := ""
			for j := 0; j < int(n.NamedChildCount()); j++ {
				c := n.NamedChild(j)
				if parser.NodeType(c, e.lang) == "identifier" {
					name = c.Text(src)
					break
				}
			}
			add(name, graph.KindFunction,
				int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1)
		}
	}

	// Call edges: function_call nodes.
	funcRanges := buildFuncRanges(result)
	walkNodes(root, func(n *sitter.Node) {
		if parser.NodeType(n, e.lang) != "function_call" {
			return
		}
		name := ""
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if parser.NodeType(c, e.lang) == "identifier" {
				name = c.Text(src)
				break
			}
		}
		if name == "" || isGleamKeyword(name) {
			return
		}
		line := int(n.StartPoint().Row) + 1
		callerID := findEnclosingFunc(funcRanges, line)
		if callerID == "" || strings.HasSuffix(callerID, "::"+name) {
			return
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: callerID, To: "unresolved::" + name,
			Kind: graph.EdgeCalls, FilePath: filePath, Line: line,
		})
	})

	return result, nil
}

func isGleamKeyword(s string) bool {
	switch s {
	case "let", "const", "fn", "pub", "import", "type", "as",
		"case", "if", "else", "use", "external", "opaque",
		"assert", "panic", "todo", "True", "False", "Nil":
		return true
	}
	return false
}

var _ parser.Extractor = (*GleamExtractor)(nil)
