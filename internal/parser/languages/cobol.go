package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// COBOL extraction captures PROGRAM-ID, DIVISION and SECTION headers,
// `CALL 'name'` subprogram calls, and `COPY name` library imports.
// Paragraph names are skipped.
//
// The odvcencio COBOL grammar exposes:
//   start → program_definition
//     identification_division(program_name)
//     data_division(working_storage_section, linkage_section, …)
//     procedure_division(section_header, call_statement, copy_statement, …)
// Section names printed as `FOO SECTION.` are not captured as named
// nodes by the grammar; the adapter recovers them from the source line
// prefix preceding the `section_header` node's start column.

// CobolExtractor extracts COBOL source files into graph nodes and edges.
type CobolExtractor struct {
	lang *sitter.Language
}

func NewCobolExtractor() *CobolExtractor {
	return &CobolExtractor{lang: grammars.CobolLanguage()}
}

func (e *CobolExtractor) Language() string { return "cobol" }
func (e *CobolExtractor) Extensions() []string {
	return []string{".cob", ".cbl", ".cpy", ".COB", ".CBL", ".CPY"}
}

func (e *CobolExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		FilePath: filePath, StartLine: 1, EndLine: int(root.EndPoint().Row) + 1,
		Language: "cobol",
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
			Language: "cobol",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	endLine := int(root.EndPoint().Row) + 1

	walkNodes(root, func(node *sitter.Node) {
		line := int(node.StartPoint().Row) + 1
		t := parser.NodeType(node, e.lang)

		switch t {
		case "identification_division":
			// Find program_name child and add as a function symbol.
			for i := 0; i < int(node.ChildCount()); i++ {
				child := node.Child(i)
				if child != nil && parser.NodeType(child, e.lang) == "program_name" {
					name := child.Text(src)
					add(name, graph.KindFunction, int(child.StartPoint().Row)+1, endLine)
					break
				}
			}
			add("IDENTIFICATION-DIVISION", graph.KindType, line, line)

		case "data_division":
			add("DATA-DIVISION", graph.KindType, line, line)

		case "procedure_division":
			add("PROCEDURE-DIVISION", graph.KindType, line, line)

		case "environment_division":
			add("ENVIRONMENT-DIVISION", graph.KindType, line, line)

		case "working_storage_section":
			add("WORKING-STORAGE-SECTION", graph.KindMethod, line, line)

		case "linkage_section":
			add("LINKAGE-SECTION", graph.KindMethod, line, line)

		case "file_section":
			add("FILE-SECTION", graph.KindMethod, line, line)

		case "section_header":
			// The grammar drops the section name token; recover it from the
			// source line prefix preceding the node's start column.
			row := int(node.StartPoint().Row)
			col := int(node.StartPoint().Column)
			if row >= 0 && row < len(lines) {
				prefix := lines[row]
				if col <= len(prefix) {
					prefix = prefix[:col]
				}
				name := strings.TrimSpace(prefix)
				if name != "" {
					add(strings.ToUpper(name)+"-SECTION", graph.KindMethod, row+1, row+1)
				}
			}

		case "copy_statement":
			// COPY WORD.
			for i := 0; i < int(node.ChildCount()); i++ {
				child := node.Child(i)
				if child != nil && parser.NodeType(child, e.lang) == "WORD" {
					mod := child.Text(src)
					result.Edges = append(result.Edges, &graph.Edge{
						From: fileNode.ID, To: "unresolved::import::" + mod,
						Kind: graph.EdgeImports, FilePath: filePath, Line: line,
					})
					break
				}
			}

		case "call_statement":
			// CALL 'NAME' ... — first string child is the target.
			for i := 0; i < int(node.ChildCount()); i++ {
				child := node.Child(i)
				if child != nil && parser.NodeType(child, e.lang) == "string" {
					target := strings.Trim(child.Text(src), `"'`)
					if target == "" {
						break
					}
					result.Edges = append(result.Edges, &graph.Edge{
						From: fileNode.ID, To: "unresolved::" + target,
						Kind: graph.EdgeCalls, FilePath: filePath, Line: line,
					})
					break
				}
			}
		}
	})

	return result, nil
}

var _ parser.Extractor = (*CobolExtractor)(nil)
