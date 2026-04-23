package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// GroovyExtractor extracts Groovy / Gradle source using tree-sitter.
//
// The odvcencio grammar conflates class / interface / enum / trait into
// a single `class_definition` node (the keyword itself is anonymous),
// so we peek at the raw source text before the identifier to pick the
// right graph Kind.
type GroovyExtractor struct {
	lang *sitter.Language
}

func NewGroovyExtractor() *GroovyExtractor {
	return &GroovyExtractor{lang: grammars.GroovyLanguage()}
}

func (e *GroovyExtractor) Language() string     { return "groovy" }
func (e *GroovyExtractor) Extensions() []string { return []string{".groovy", ".gvy", ".gy", ".gradle"} }

func (e *GroovyExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "groovy",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	add := func(name string, kind graph.NodeKind, start, end int) {
		if name == "" || groovyIsKeyword(name) {
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
			Language: "groovy",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	walkNodes(root, func(n *sitter.Node) {
		t := parser.NodeType(n, e.lang)
		switch t {
		case "groovy_import":
			// The `qualified_name` child carries the dotted path.
			name := ""
			for i := 0; i < int(n.NamedChildCount()); i++ {
				c := n.NamedChild(i)
				if parser.NodeType(c, e.lang) == "qualified_name" {
					name = c.Text(src)
					break
				}
			}
			if name == "" {
				return
			}
			line := int(n.StartPoint().Row) + 1
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: "unresolved::import::" + name,
				Kind: graph.EdgeImports, FilePath: filePath, Line: line,
			})

		case "class_definition":
			// The keyword (class / interface / enum / trait) is
			// anonymous. Look at the bytes before the identifier child
			// to figure out which.
			var identNode *sitter.Node
			for i := 0; i < int(n.NamedChildCount()); i++ {
				c := n.NamedChild(i)
				if parser.NodeType(c, e.lang) == "identifier" {
					identNode = c
					break
				}
			}
			if identNode == nil {
				return
			}
			name := identNode.Text(src)
			start := int(n.StartPoint().Row) + 1
			end := int(n.EndPoint().Row) + 1

			kind := graph.KindType
			prefix := strings.ToLower(string(src[n.StartByte():identNode.StartByte()]))
			if strings.Contains(prefix, "interface") {
				kind = graph.KindInterface
			}
			add(name, kind, start, end)

		case "function_definition", "function_declaration":
			// The name is the first identifier child (after any
			// modifiers).
			var nameNode *sitter.Node
			for i := 0; i < int(n.NamedChildCount()); i++ {
				c := n.NamedChild(i)
				if parser.NodeType(c, e.lang) == "identifier" {
					nameNode = c
					break
				}
			}
			if nameNode == nil {
				return
			}
			add(nameNode.Text(src), graph.KindFunction,
				int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1)
		}
	})

	return result, nil
}

func groovyIsKeyword(s string) bool {
	switch s {
	case "class", "interface", "enum", "trait", "def", "static",
		"public", "private", "protected", "abstract", "final",
		"import", "package", "return", "if", "else", "for", "while",
		"do", "try", "catch", "finally", "throw", "throws", "new",
		"this", "super", "null", "true", "false", "void", "int",
		"long", "short", "byte", "char", "float", "double", "boolean",
		"String", "as", "in", "is", "instanceof":
		return true
	}
	return false
}

var _ parser.Extractor = (*GroovyExtractor)(nil)
