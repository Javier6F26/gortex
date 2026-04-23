package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// CMake is command-call-structured. `function(NAME ...)` /
// `macro(NAME ...)` introduce callable blocks terminated by
// `endfunction()` / `endmacro()`; `add_library` / `add_executable`
// declare build targets (modelled as function nodes); `set(NAME ...)`
// declares variables; `include(...)` and `add_subdirectory(...)`
// are imports.
//
// The odvcencio CMake grammar exposes:
//   source_file
//     normal_command      identifier, argument_list(argument(unquoted_argument))
//     function_def        function_command(function, argument_list), body, endfunction_command
//     macro_def           macro_command(macro, argument_list), body, endmacro_command
// so the extraction walks top-level children and dispatches on node type.

// CMakeExtractor extracts CMake source files into graph nodes and edges.
type CMakeExtractor struct {
	lang *sitter.Language
}

func NewCMakeExtractor() *CMakeExtractor {
	return &CMakeExtractor{lang: grammars.CmakeLanguage()}
}

func (e *CMakeExtractor) Language() string     { return "cmake" }
func (e *CMakeExtractor) Extensions() []string { return []string{".cmake", "CMakeLists.txt"} }

func (e *CMakeExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "cmake",
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
			Language: "cmake",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	emitImport := func(mod string, line int) {
		mod = strings.Trim(mod, `"`)
		if mod == "" {
			return
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + mod,
			Kind: graph.EdgeImports, FilePath: filePath, Line: line,
		})
	}

	// Walk the whole tree — function_def / macro_def may also appear
	// inside outer conditionals, and normal_command can occur nested too.
	walkNodes(root, func(node *sitter.Node) {
		switch parser.NodeType(node, e.lang) {
		case "function_def":
			name, line := e.cmakeDefName(node, src, "function_command")
			add(name, graph.KindFunction, line, int(node.EndPoint().Row)+1)
		case "macro_def":
			name, line := e.cmakeDefName(node, src, "macro_command")
			add(name, graph.KindFunction, line, int(node.EndPoint().Row)+1)
		case "normal_command":
			e.handleNormalCommand(node, src, add, emitImport)
		}
	})

	return result, nil
}

// cmakeDefName finds the first argument's unquoted_argument text inside
// the header command (function_command or macro_command) of a def.
func (e *CMakeExtractor) cmakeDefName(defNode *sitter.Node, src []byte, headerType string) (string, int) {
	for i := 0; i < int(defNode.ChildCount()); i++ {
		header := defNode.Child(i)
		if header == nil || parser.NodeType(header, e.lang) != headerType {
			continue
		}
		return e.firstArgText(header, src), int(header.StartPoint().Row) + 1
	}
	return "", int(defNode.StartPoint().Row) + 1
}

// handleNormalCommand dispatches on the leading identifier of a
// normal_command node: set / add_library / add_executable define
// symbols; include / add_subdirectory emit import edges.
func (e *CMakeExtractor) handleNormalCommand(
	node *sitter.Node, src []byte,
	add func(name string, kind graph.NodeKind, start, end int),
	emitImport func(mod string, line int),
) {
	cmdName := ""
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child != nil && parser.NodeType(child, e.lang) == "identifier" {
			cmdName = strings.ToLower(child.Text(src))
			break
		}
	}
	if cmdName == "" {
		return
	}
	firstArg := e.firstArgText(node, src)
	line := int(node.StartPoint().Row) + 1
	switch cmdName {
	case "set":
		add(firstArg, graph.KindVariable, line, line)
	case "add_library", "add_executable":
		add(firstArg, graph.KindFunction, line, line)
	case "include", "add_subdirectory":
		emitImport(firstArg, line)
	}
}

// firstArgText returns the text of the first `argument` inside the
// command's argument_list. Strips quotes.
func (e *CMakeExtractor) firstArgText(header *sitter.Node, src []byte) string {
	for i := 0; i < int(header.ChildCount()); i++ {
		child := header.Child(i)
		if child == nil || parser.NodeType(child, e.lang) != "argument_list" {
			continue
		}
		for j := 0; j < int(child.ChildCount()); j++ {
			arg := child.Child(j)
			if arg == nil || parser.NodeType(arg, e.lang) != "argument" {
				continue
			}
			return strings.Trim(arg.Text(src), `"`)
		}
	}
	return ""
}

var _ parser.Extractor = (*CMakeExtractor)(nil)
