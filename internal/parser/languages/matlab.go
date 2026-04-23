package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// MATLAB / Octave. The tree-sitter grammar emits `function_definition`,
// `class_definition`, `command` (for bare statements like `import pkg.*`),
// and `function_call`. We register only the `.mlx` extension so the
// Objective-C extractor keeps priority on the ambiguous `.m` extension;
// the adapter registry handles ordering.

// MatlabExtractor extracts MATLAB/Octave source with tree-sitter.
type MatlabExtractor struct {
	lang *sitter.Language
}

func NewMatlabExtractor() *MatlabExtractor {
	return &MatlabExtractor{lang: grammars.MatlabLanguage()}
}

func (e *MatlabExtractor) Language() string     { return "matlab" }
func (e *MatlabExtractor) Extensions() []string { return []string{".mlx"} }

func (e *MatlabExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	tree, err := parser.ParseFile(src, e.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	root := tree.RootNode()
	result := &parser.ExtractionResult{}

	endLine := int(root.EndPoint().Row) + 1
	if endLine < 1 {
		endLine = 1
	}
	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: endLine,
		Language: "matlab",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	add := func(name string, kind graph.NodeKind, start, end int) {
		if name == "" || isMatlabKeyword(name) {
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
			Language: "matlab",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	// Walk the tree for function/class definitions and imports.
	walkNodes(root, func(node *sitter.Node) {
		switch parser.NodeType(node, e.lang) {
		case "function_definition":
			name := e.matlabFuncName(node, src)
			if name == "" {
				return
			}
			add(name, graph.KindFunction,
				int(node.StartPoint().Row)+1, int(node.EndPoint().Row)+1)
		case "class_definition":
			name := e.matlabIdent(node, src)
			if name == "" {
				return
			}
			add(name, graph.KindType,
				int(node.StartPoint().Row)+1, int(node.EndPoint().Row)+1)
		case "command":
			// `import matlab.io.*` — command_name "import" followed by
			// command_argument holding the module path.
			cmdName, cmdArg := e.matlabCommand(node, src)
			if cmdName != "import" || cmdArg == "" {
				return
			}
			mod := strings.TrimSuffix(cmdArg, ".*")
			mod = strings.TrimSpace(mod)
			if mod == "" {
				return
			}
			line := int(node.StartPoint().Row) + 1
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: "unresolved::import::" + mod,
				Kind: graph.EdgeImports, FilePath: filePath, Line: line,
			})
		}
	})

	// Call sites inside functions.
	funcRanges := buildFuncRanges(result)
	walkNodes(root, func(node *sitter.Node) {
		if parser.NodeType(node, e.lang) != "function_call" {
			return
		}
		name := ""
		for i := 0; i < int(node.ChildCount()); i++ {
			c := node.Child(i)
			if c == nil {
				continue
			}
			if parser.NodeType(c, e.lang) == "identifier" {
				name = c.Text(src)
				break
			}
		}
		if name == "" || isMatlabKeyword(name) {
			return
		}
		line := int(node.StartPoint().Row) + 1
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

// matlabFuncName extracts the identifier that names the function. The
// grammar shape is
//
//	function_definition
//	  "function"
//	  function_output?   (e.g. `y =` or `[a, b] =`)
//	  identifier         <- function name
//	  function_arguments?
//	  ...
func (e *MatlabExtractor) matlabFuncName(node *sitter.Node, src []byte) string {
	sawFunction := false
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		t := parser.NodeType(c, e.lang)
		if t == "function" {
			sawFunction = true
			continue
		}
		if !sawFunction {
			continue
		}
		if t == "identifier" {
			return c.Text(src)
		}
		// Skip function_output, whitespace, anonymous tokens.
	}
	return ""
}

// matlabIdent returns the first direct-child identifier of a node.
func (e *MatlabExtractor) matlabIdent(node *sitter.Node, src []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, e.lang) == "identifier" {
			return c.Text(src)
		}
	}
	return ""
}

// matlabCommand returns (commandName, firstArgument) for a `command` node
// like `import matlab.io.*`.
func (e *MatlabExtractor) matlabCommand(node *sitter.Node, src []byte) (string, string) {
	name, arg := "", ""
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		switch parser.NodeType(c, e.lang) {
		case "command_name":
			name = strings.TrimSpace(c.Text(src))
		case "command_argument":
			if arg == "" {
				arg = strings.TrimSpace(c.Text(src))
			}
		}
	}
	return name, arg
}

func isMatlabKeyword(s string) bool {
	switch s {
	case "if", "elseif", "else", "end", "for", "while", "switch", "case",
		"otherwise", "break", "continue", "return", "function", "classdef",
		"properties", "methods", "events", "enumeration", "global",
		"persistent", "try", "catch", "parfor", "spmd", "import":
		return true
	}
	return false
}

var _ parser.Extractor = (*MatlabExtractor)(nil)
