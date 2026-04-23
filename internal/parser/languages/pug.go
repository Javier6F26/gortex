package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// PugExtractor extracts Pug/Jade templates.
//
// The odvcencio Pug grammar is young and emits many ERROR nodes for
// regular tag content — but the structural pieces we care about
// (`mixin_definition`, `block_definition`, `extends`, `include`) are
// exposed cleanly. We walk the whole tree to pick them out regardless
// of how deeply ERRORs wrap them.
//
// `extends` / `include` use a `filename` child whose text includes the
// leading whitespace that follows the keyword; we trim it.
type PugExtractor struct {
	lang *sitter.Language
}

func NewPugExtractor() *PugExtractor {
	return &PugExtractor{lang: grammars.PugLanguage()}
}

func (e *PugExtractor) Language() string     { return "pug" }
func (e *PugExtractor) Extensions() []string { return []string{".pug", ".jade"} }

func (e *PugExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	tree, err := parser.ParseFile(src, e.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	root := tree.RootNode()
	result := &parser.ExtractionResult{}

	endLine := 1
	if root != nil {
		endLine = int(root.EndPoint().Row) + 1
	}
	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: endLine,
		Language: "pug",
	}
	result.Nodes = append(result.Nodes, fileNode)

	if root == nil {
		return result, nil
	}

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
			Language: "pug",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	walkNodes(root, func(n *sitter.Node) {
		switch parser.NodeType(n, e.lang) {
		case "mixin_definition":
			// tag_name holds the mixin's name.
			name := firstChildOfTypeText(n, "tag_name", src, e.lang)
			line := int(n.StartPoint().Row) + 1
			end := int(n.EndPoint().Row) + 1
			add(name, graph.KindFunction, line, end)

		case "block_definition":
			// `block_name` holds the block's label.
			name := firstChildOfTypeText(n, "block_name", src, e.lang)
			line := int(n.StartPoint().Row) + 1
			end := int(n.EndPoint().Row) + 1
			add(name, graph.KindFunction, line, end)

		case "extends", "include":
			line := int(n.StartPoint().Row) + 1
			mod := strings.TrimSpace(firstChildOfTypeText(n, "filename", src, e.lang))
			if mod == "" {
				return
			}
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: "unresolved::import::" + mod,
				Kind: graph.EdgeImports, FilePath: filePath, Line: line,
			})
		}
	})

	return result, nil
}

var _ parser.Extractor = (*PugExtractor)(nil)
