package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// HTMLExtractor extracts HTML files into graph nodes and edges.
type HTMLExtractor struct {
	lang *sitter.Language
}

func NewHTMLExtractor() *HTMLExtractor {
	return &HTMLExtractor{lang: grammars.HtmlLanguage()}
}

func (e *HTMLExtractor) Language() string     { return "html" }
func (e *HTMLExtractor) Extensions() []string { return []string{".html", ".htm"} }

func (e *HTMLExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "html",
	}
	result.Nodes = append(result.Nodes, fileNode)

	// Walk the AST manually since HTML tree-sitter queries can be quirky.
	e.walkNode(root, src, filePath, fileNode.ID, result)

	return result, nil
}

func (e *HTMLExtractor) walkNode(node *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult) {
	nodeType := parser.NodeType(node, e.lang)

	switch nodeType {
	case "script_element":
		e.extractScriptImport(node, src, filePath, fileID, result)
	case "element":
		e.extractElement(node, src, filePath, fileID, result)
	}

	// Recurse into children.
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child != nil {
			e.walkNode(child, src, filePath, fileID, result)
		}
	}
}

// extractScriptImport checks a script_element for a src attribute.
func (e *HTMLExtractor) extractScriptImport(node *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult) {
	startTag := findChildByType(node, "start_tag", e.lang)
	if startTag == nil {
		// Self-closing script tag.
		startTag = findChildByType(node, "self_closing_tag", e.lang)
	}
	if startTag == nil {
		return
	}

	srcAttr := findAttribute(startTag, "src", src, e.lang)
	if srcAttr == "" {
		return
	}

	result.Edges = append(result.Edges, &graph.Edge{
		From:     fileID,
		To:       "unresolved::import::" + srcAttr,
		Kind:     graph.EdgeImports,
		FilePath: filePath,
		Line:     int(node.StartPoint().Row) + 1,
	})
}

// extractElement checks elements for link tags (stylesheet imports) and id attributes.
func (e *HTMLExtractor) extractElement(node *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult) {
	startTag := findChildByType(node, "start_tag", e.lang)
	if startTag == nil {
		startTag = findChildByType(node, "self_closing_tag", e.lang)
	}
	if startTag == nil {
		return
	}

	tagName := findChildByType(startTag, "tag_name", e.lang)
	if tagName == nil {
		return
	}
	tag := tagName.Text(src)

	// Link/stylesheet imports.
	if tag == "link" {
		href := findAttribute(startTag, "href", src, e.lang)
		if href != "" {
			result.Edges = append(result.Edges, &graph.Edge{
				From:     fileID,
				To:       "unresolved::import::" + href,
				Kind:     graph.EdgeImports,
				FilePath: filePath,
				Line:     int(node.StartPoint().Row) + 1,
			})
		}
	}

	// Elements with id attributes.
	idVal := findAttribute(startTag, "id", src, e.lang)
	if idVal != "" {
		id := filePath + "::#" + idVal
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindVariable, Name: idVal,
			FilePath: filePath, StartLine: int(node.StartPoint().Row) + 1, EndLine: int(node.EndPoint().Row) + 1,
			Language: "html", Meta: map[string]any{
				"tag":  tag,
				"html": "id",
			},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: int(node.StartPoint().Row) + 1,
		})
	}
}

// findChildByType finds the first child node with the given type.
func findChildByType(node *sitter.Node, typeName string, lang *sitter.Language) *sitter.Node {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child != nil && parser.NodeType(child, lang) == typeName {
			return child
		}
	}
	return nil
}

// findAttribute looks for an attribute with the given name in a start_tag node
// and returns its unquoted value.
func findAttribute(startTag *sitter.Node, attrName string, src []byte, lang *sitter.Language) string {
	for i := 0; i < int(startTag.ChildCount()); i++ {
		child := startTag.Child(i)
		if child == nil || parser.NodeType(child, lang) != "attribute" {
			continue
		}
		nameNode := findChildByType(child, "attribute_name", lang)
		if nameNode == nil || nameNode.Text(src) != attrName {
			continue
		}
		valNode := findChildByType(child, "quoted_attribute_value", lang)
		if valNode == nil {
			continue
		}
		val := valNode.Text(src)
		val = strings.Trim(val, `"'`)
		return val
	}
	return ""
}
