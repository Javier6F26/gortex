package languages

import (
	"fmt"
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

const (
	jsQFunction = `(function_declaration
		name: (identifier) @func.name) @func.def`

	jsQArrow = `(lexical_declaration
		(variable_declarator
			name: (identifier) @func.name
			value: (arrow_function) @func.body)) @func.def`

	jsQClass = `(class_declaration
		name: (identifier) @class.name) @class.def`

	jsQMethod = `(method_definition
		name: (property_identifier) @method.name) @method.def`

	jsQImport = `(import_statement
		source: (string (string_fragment) @import.path)) @import.def`

	jsQRequire = `(call_expression
		function: (identifier) @req.name
		arguments: (arguments (string (string_fragment) @req.path))) @req.def`

	jsQCall = `(call_expression
		function: (identifier) @call.name) @call.expr`

	jsQCallMember = `(call_expression
		function: (member_expression
			property: (property_identifier) @call.method)) @call.expr`

	jsQVar = `(lexical_declaration
		(variable_declarator
			name: (identifier) @var.name)) @var.def`

	jsQVarDecl = `(variable_declaration
		(variable_declarator
			name: (identifier) @var.name)) @var.def`

	jsQExport = `(export_statement
		(function_declaration
			name: (identifier) @func.name)) @func.def`
)

// JavaScriptExtractor extracts JavaScript source files.
type JavaScriptExtractor struct {
	lang *sitter.Language
}

func NewJavaScriptExtractor() *JavaScriptExtractor {
	return &JavaScriptExtractor{lang: grammars.JavascriptLanguage()}
}

func (e *JavaScriptExtractor) Language() string     { return "javascript" }
func (e *JavaScriptExtractor) Extensions() []string { return []string{".js", ".jsx", ".mjs"} }

func (e *JavaScriptExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "javascript",
	}
	result.Nodes = append(result.Nodes, fileNode)

	// Functions.
	for _, q := range []string{jsQFunction, jsQExport} {
		e.extractFuncs(q, root, src, filePath, fileNode.ID, result)
	}

	// Arrow functions assigned to variables.
	e.extractArrowFuncs(root, src, filePath, fileNode.ID, result)

	// Classes.
	e.extractClasses(root, src, filePath, fileNode.ID, result)

	// Imports.
	e.extractImports(root, src, filePath, fileNode.ID, result)

	// Require calls.
	e.extractRequires(root, src, filePath, fileNode.ID, result)

	// Call sites.
	e.extractCalls(root, src, filePath, result)

	// Variables.
	e.extractVariables(root, src, filePath, fileNode.ID, result)

	return result, nil
}

func (e *JavaScriptExtractor) extractFuncs(q string, root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult) {
	matches, _ := parser.RunQuery(q, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["func.name"].Text
		def := m.Captures["func.def"]
		id := filePath + "::" + name
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindFunction, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "javascript", Meta: map[string]any{"signature": fmt.Sprintf("function %s()", name)},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}
}

func (e *JavaScriptExtractor) extractArrowFuncs(root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult) {
	matches, _ := parser.RunQuery(jsQArrow, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["func.name"].Text
		def := m.Captures["func.def"]
		id := filePath + "::" + name
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindFunction, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "javascript", Meta: map[string]any{"signature": fmt.Sprintf("const %s = () =>", name)},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}
}

func (e *JavaScriptExtractor) extractClasses(root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult) {
	matches, _ := parser.RunQuery(jsQClass, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["class.name"].Text
		def := m.Captures["class.def"]
		id := filePath + "::" + name
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindType, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "javascript",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})

		// Methods inside the class.
		e.extractMethods(def.Node, src, filePath, id, result)
	}
}

func (e *JavaScriptExtractor) extractMethods(classNode *sitter.Node, src []byte, filePath, classID string, result *parser.ExtractionResult) {
	matches, _ := parser.RunQuery(jsQMethod, e.lang, classNode, src)
	for _, m := range matches {
		name := m.Captures["method.name"].Text
		def := m.Captures["method.def"]
		id := filePath + "::" + classID[strings.LastIndex(classID, "::")+2:] + "." + name
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindMethod, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "javascript",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: id, To: classID, Kind: graph.EdgeMemberOf, FilePath: filePath, Line: def.StartLine + 1,
		})
	}
}

func (e *JavaScriptExtractor) extractImports(root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult) {
	matches, _ := parser.RunQuery(jsQImport, e.lang, root, src)
	for _, m := range matches {
		importPath := m.Captures["import.path"].Text
		line := m.Captures["import.def"].StartLine + 1
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: "unresolved::import::" + importPath,
			Kind: graph.EdgeImports, FilePath: filePath, Line: line,
		})
	}
}

func (e *JavaScriptExtractor) extractRequires(root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult) {
	matches, _ := parser.RunQuery(jsQRequire, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["req.name"].Text
		if name != "require" {
			continue
		}
		reqPath := m.Captures["req.path"].Text
		line := m.Captures["req.def"].StartLine + 1
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: "unresolved::import::" + reqPath,
			Kind: graph.EdgeImports, FilePath: filePath, Line: line,
		})
	}
}

func (e *JavaScriptExtractor) extractCalls(root *sitter.Node, src []byte, filePath string, result *parser.ExtractionResult) {
	funcRanges := buildFuncRanges(result)

	matches, _ := parser.RunQuery(jsQCall, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["call.name"].Text
		expr := m.Captures["call.expr"]
		callerID := findEnclosingFunc(funcRanges, expr.StartLine+1)
		if callerID == "" {
			continue
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: callerID, To: "unresolved::" + name,
			Kind: graph.EdgeCalls, FilePath: filePath, Line: expr.StartLine + 1,
		})
	}

	matches, _ = parser.RunQuery(jsQCallMember, e.lang, root, src)
	for _, m := range matches {
		method := m.Captures["call.method"].Text
		expr := m.Captures["call.expr"]
		callerID := findEnclosingFunc(funcRanges, expr.StartLine+1)
		if callerID == "" {
			continue
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: callerID, To: "unresolved::*." + method,
			Kind: graph.EdgeCalls, FilePath: filePath, Line: expr.StartLine + 1,
		})
	}
}

func (e *JavaScriptExtractor) extractVariables(root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult) {
	// Collect names already extracted as arrow functions so we skip them.
	arrowNames := make(map[string]bool)
	for _, n := range result.Nodes {
		if n.Kind == graph.KindFunction && n.FilePath == filePath {
			arrowNames[n.Name] = true
		}
	}

	for _, q := range []string{jsQVar, jsQVarDecl} {
		matches, _ := parser.RunQuery(q, e.lang, root, src)
		for _, m := range matches {
			name := m.Captures["var.name"].Text
			def := m.Captures["var.def"]

			// Skip variables already captured as arrow functions.
			if arrowNames[name] {
				continue
			}

			// Only extract module-level variables.
			parent := def.Node.Parent()
			if parent != nil && parser.NodeType(parent, e.lang) == "export_statement" {
				parent = parent.Parent()
			}
			if parent == nil || parser.NodeType(parent, e.lang) != "program" {
				continue
			}

			id := filePath + "::" + name
			result.Nodes = append(result.Nodes, &graph.Node{
				ID: id, Kind: graph.KindVariable, Name: name,
				FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
				Language: "javascript",
			})
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
			})
		}
	}
}
