package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// PowerShellExtractor extracts PowerShell source files.
//
// The odvcencio grammar emits:
//   - `function_statement` — `function`/`filter` keyword +
//     `function_name` + body block.
//   - `class_statement` — `simple_name` + `{` + members.
//   - `command` — top-level command invocations. `Import-Module` is
//     a regular command whose first `command_elements` carries the
//     module name as a `generic_token`. Dot-source statements carry
//     a `command_invokation_operator` of "." and a `command_name_expr`
//     with the file path.
type PowerShellExtractor struct {
	lang *sitter.Language
}

func NewPowerShellExtractor() *PowerShellExtractor {
	return &PowerShellExtractor{lang: grammars.PowershellLanguage()}
}

func (e *PowerShellExtractor) Language() string { return "powershell" }
func (e *PowerShellExtractor) Extensions() []string {
	return []string{".ps1", ".psm1", ".psd1"}
}

func (e *PowerShellExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "powershell",
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
			Language: "powershell",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	walkNodes(root, func(n *sitter.Node) {
		switch parser.NodeType(n, e.lang) {
		case "function_statement":
			name := firstChildOfTypeText(n, "function_name", src, e.lang)
			line := int(n.StartPoint().Row) + 1
			end := int(n.EndPoint().Row) + 1
			add(name, graph.KindFunction, line, end)

		case "class_statement":
			name := firstChildOfTypeText(n, "simple_name", src, e.lang)
			line := int(n.StartPoint().Row) + 1
			end := int(n.EndPoint().Row) + 1
			add(name, graph.KindType, line, end)

		case "command":
			e.handleCommand(n, src, filePath, fileNode, result)
		}
	})

	return result, nil
}

// handleCommand picks import-like commands out of a `command` node:
// `Import-Module <Name>` and dot-source `. <path>`. Everything else is
// ignored — this keeps the extractor close to the regex version which
// only captured these two shapes.
func (e *PowerShellExtractor) handleCommand(
	node *sitter.Node, src []byte, filePath string, fileNode *graph.Node,
	result *parser.ExtractionResult,
) {
	line := int(node.StartPoint().Row) + 1

	// Dot-source statement: `. path` — first child is
	// `command_invokation_operator` with text ".".
	var op, nameExpr, cmdName string
	var firstElemArg string
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		switch parser.NodeType(child, e.lang) {
		case "command_invokation_operator":
			op = strings.TrimSpace(child.Text(src))
		case "command_name_expr":
			nameExpr = strings.TrimSpace(child.Text(src))
		case "command_name":
			cmdName = strings.TrimSpace(child.Text(src))
		case "command_elements":
			// First non-separator child's text.
			if firstElemArg == "" {
				for j := 0; j < int(child.ChildCount()); j++ {
					sub := child.Child(j)
					if sub == nil {
						continue
					}
					st := parser.NodeType(sub, e.lang)
					if st == "command_argument_sep" {
						continue
					}
					firstElemArg = strings.Trim(strings.TrimSpace(sub.Text(src)), `"'`)
					break
				}
			}
		}
	}

	if op == "." && nameExpr != "" {
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + nameExpr,
			Kind: graph.EdgeImports, FilePath: filePath, Line: line,
		})
		return
	}

	if strings.EqualFold(cmdName, "Import-Module") && firstElemArg != "" {
		// Skip `-Name` flag if present — the argument is the following token.
		if strings.EqualFold(firstElemArg, "-Name") {
			// Find the next non-separator element.
			firstElemArg = ""
			for i := 0; i < int(node.ChildCount()); i++ {
				child := node.Child(i)
				if child == nil || parser.NodeType(child, e.lang) != "command_elements" {
					continue
				}
				found := false
				for j := 0; j < int(child.ChildCount()); j++ {
					sub := child.Child(j)
					if sub == nil {
						continue
					}
					st := parser.NodeType(sub, e.lang)
					if st == "command_argument_sep" {
						continue
					}
					txt := strings.Trim(strings.TrimSpace(sub.Text(src)), `"'`)
					if !found {
						if strings.EqualFold(txt, "-Name") {
							found = true
						}
						continue
					}
					firstElemArg = txt
					break
				}
				if firstElemArg != "" {
					break
				}
			}
		}
		if firstElemArg != "" {
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: "unresolved::import::" + firstElemArg,
				Kind: graph.EdgeImports, FilePath: filePath, Line: line,
			})
		}
	}
}

var _ parser.Extractor = (*PowerShellExtractor)(nil)
