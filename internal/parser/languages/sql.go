package languages

import (
	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// SQLExtractor extracts SQL source files.
type SQLExtractor struct {
	lang *sitter.Language
}

func NewSQLExtractor() *SQLExtractor {
	return &SQLExtractor{lang: grammars.SqlLanguage()}
}

func (e *SQLExtractor) Language() string     { return "sql" }
func (e *SQLExtractor) Extensions() []string { return []string{".sql"} }

func (e *SQLExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "sql",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)

	// Walk top-level statements. odvcencio's SQL grammar emits the
	// CREATE variants directly under the source_file (or nested inside
	// a wrapping "statement" node depending on the dialect). Handle
	// both shapes so the extractor is robust across grammar versions.
	var visit func(n *sitter.Node)
	visit = func(n *sitter.Node) {
		switch parser.NodeType(n, e.lang) {
		case "create_table_statement", "create_table":
			e.extractCreateTable(n, src, filePath, fileNode.ID, seen, result)
			return
		case "create_view_statement", "create_view":
			e.extractCreateView(n, src, filePath, fileNode.ID, seen, result)
			return
		case "create_function_statement", "create_function":
			e.extractCreateFunction(n, src, filePath, fileNode.ID, seen, result)
			return
		case "create_index_statement", "create_index":
			e.extractCreateIndex(n, src, filePath, fileNode.ID, seen, result)
			return
		case "create_trigger_statement", "create_trigger":
			e.extractCreateTrigger(n, src, filePath, fileNode.ID, seen, result)
			return
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			visit(n.NamedChild(i))
		}
	}
	visit(root)

	return result, nil
}

func (e *SQLExtractor) extractCreateTable(node *sitter.Node, src []byte, filePath, fileID string, seen map[string]bool, result *parser.ExtractionResult) {
	name := findObjectName(node, src, e.lang)
	if name == "" || seen[name] {
		return
	}
	seen[name] = true
	id := filePath + "::" + name
	result.Nodes = append(result.Nodes, &graph.Node{
		ID: id, Kind: graph.KindType, Name: name,
		FilePath: filePath, StartLine: int(node.StartPoint().Row) + 1, EndLine: int(node.EndPoint().Row) + 1,
		Language: "sql", Meta: map[string]any{"sql_type": "table"},
	})
	result.Edges = append(result.Edges, &graph.Edge{
		From: fileID, To: id, Kind: graph.EdgeDefines,
		FilePath: filePath, Line: int(node.StartPoint().Row) + 1,
	})

	// Extract column names as variables with EdgeMemberOf. The odvcencio
	// grammar wraps them in "table_parameters" → "table_column"; the
	// older naming ("column_definitions" → "column_definition") is kept
	// so any legacy parse path still works.
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		switch parser.NodeType(child, e.lang) {
		case "table_parameters", "column_definitions":
			for k := 0; k < int(child.NamedChildCount()); k++ {
				col := child.NamedChild(k)
				colType := parser.NodeType(col, e.lang)
				if colType != "table_column" && colType != "column_definition" {
					continue
				}
				colName := firstNamedChildOfType(col, "identifier", src, e.lang)
				if colName == "" {
					continue
				}
				colID := id + "." + colName
				if seen[colID] {
					continue
				}
				seen[colID] = true
				result.Nodes = append(result.Nodes, &graph.Node{
					ID: colID, Kind: graph.KindVariable, Name: colName,
					FilePath: filePath, StartLine: int(col.StartPoint().Row) + 1, EndLine: int(col.EndPoint().Row) + 1,
					Language: "sql",
				})
				result.Edges = append(result.Edges, &graph.Edge{
					From: colID, To: id, Kind: graph.EdgeMemberOf,
					FilePath: filePath, Line: int(col.StartPoint().Row) + 1,
				})
			}
		}
	}
}

func (e *SQLExtractor) extractCreateView(node *sitter.Node, src []byte, filePath, fileID string, seen map[string]bool, result *parser.ExtractionResult) {
	name := findObjectName(node, src, e.lang)
	if name == "" || seen[name] {
		return
	}
	seen[name] = true
	id := filePath + "::" + name
	result.Nodes = append(result.Nodes, &graph.Node{
		ID: id, Kind: graph.KindType, Name: name,
		FilePath: filePath, StartLine: int(node.StartPoint().Row) + 1, EndLine: int(node.EndPoint().Row) + 1,
		Language: "sql", Meta: map[string]any{"sql_type": "view"},
	})
	result.Edges = append(result.Edges, &graph.Edge{
		From: fileID, To: id, Kind: graph.EdgeDefines,
		FilePath: filePath, Line: int(node.StartPoint().Row) + 1,
	})
}

func (e *SQLExtractor) extractCreateFunction(node *sitter.Node, src []byte, filePath, fileID string, seen map[string]bool, result *parser.ExtractionResult) {
	name := findObjectName(node, src, e.lang)
	if name == "" || seen[name] {
		return
	}
	seen[name] = true
	id := filePath + "::" + name
	result.Nodes = append(result.Nodes, &graph.Node{
		ID: id, Kind: graph.KindFunction, Name: name,
		FilePath: filePath, StartLine: int(node.StartPoint().Row) + 1, EndLine: int(node.EndPoint().Row) + 1,
		Language: "sql",
	})
	result.Edges = append(result.Edges, &graph.Edge{
		From: fileID, To: id, Kind: graph.EdgeDefines,
		FilePath: filePath, Line: int(node.StartPoint().Row) + 1,
	})
}

func (e *SQLExtractor) extractCreateIndex(node *sitter.Node, src []byte, filePath, fileID string, seen map[string]bool, result *parser.ExtractionResult) {
	name := findObjectName(node, src, e.lang)
	if name == "" || seen[name] {
		return
	}
	seen[name] = true
	id := filePath + "::" + name
	result.Nodes = append(result.Nodes, &graph.Node{
		ID: id, Kind: graph.KindVariable, Name: name,
		FilePath: filePath, StartLine: int(node.StartPoint().Row) + 1, EndLine: int(node.EndPoint().Row) + 1,
		Language: "sql", Meta: map[string]any{"sql_type": "index"},
	})
	result.Edges = append(result.Edges, &graph.Edge{
		From: fileID, To: id, Kind: graph.EdgeDefines,
		FilePath: filePath, Line: int(node.StartPoint().Row) + 1,
	})
}

func (e *SQLExtractor) extractCreateTrigger(node *sitter.Node, src []byte, filePath, fileID string, seen map[string]bool, result *parser.ExtractionResult) {
	name := findObjectName(node, src, e.lang)
	if name == "" || seen[name] {
		return
	}
	seen[name] = true
	id := filePath + "::" + name
	result.Nodes = append(result.Nodes, &graph.Node{
		ID: id, Kind: graph.KindVariable, Name: name,
		FilePath: filePath, StartLine: int(node.StartPoint().Row) + 1, EndLine: int(node.EndPoint().Row) + 1,
		Language: "sql", Meta: map[string]any{"sql_type": "trigger"},
	})
	result.Edges = append(result.Edges, &graph.Edge{
		From: fileID, To: id, Kind: graph.EdgeDefines,
		FilePath: filePath, Line: int(node.StartPoint().Row) + 1,
	})
}

// findObjectName extracts the name from a CREATE statement. The odvcencio
// grammar puts the name as a direct `identifier` child of the statement;
// older smacker-era grammars wrapped it in `object_reference`. Handle both.
func findObjectName(node *sitter.Node, src []byte, lang *sitter.Language) string {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if parser.NodeType(child, lang) == "object_reference" {
			return firstNamedChildOfType(child, "identifier", src, lang)
		}
	}
	// Direct identifier (odvcencio shape).
	return firstNamedChildOfType(node, "identifier", src, lang)
}

func firstNamedChildOfType(node *sitter.Node, nodeType string, src []byte, lang *sitter.Language) string {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if parser.NodeType(child, lang) == nodeType {
			return child.Text(src)
		}
	}
	return ""
}

var _ parser.Extractor = (*SQLExtractor)(nil)
