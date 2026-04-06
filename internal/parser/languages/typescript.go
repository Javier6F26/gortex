package languages

import (
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

const (
	tsQFunction = `(function_declaration
		name: (identifier) @func.name) @func.def`

	tsQArrow = `(lexical_declaration
		(variable_declarator
			name: (identifier) @func.name
			value: (arrow_function) @func.body)) @func.def`

	tsQClass = `(class_declaration
		name: (type_identifier) @class.name) @class.def`

	tsQInterface = `(interface_declaration
		name: (type_identifier) @iface.name) @iface.def`

	tsQTypeAlias = `(type_alias_declaration
		name: (type_identifier) @type.name) @type.def`

	tsQMethod = `(method_definition
		name: (property_identifier) @method.name) @method.def`

	tsQImport = `(import_statement
		source: (string) @import.path) @import.def`

	tsQCall = `(call_expression
		function: (identifier) @call.name) @call.expr`

	tsQCallMember = `(call_expression
		function: (member_expression
			property: (property_identifier) @call.method)) @call.expr`

	tsQVar = `(lexical_declaration
		(variable_declarator
			name: (identifier) @var.name)) @var.def`

	tsQExport = `(export_statement
		(function_declaration
			name: (identifier) @func.name)) @func.def`
)

// TypeScriptExtractor extracts TypeScript/JavaScript source files.
type TypeScriptExtractor struct {
	lang *sitter.Language
}

func NewTypeScriptExtractor() *TypeScriptExtractor {
	return &TypeScriptExtractor{lang: typescript.GetLanguage()}
}

func (e *TypeScriptExtractor) Language() string     { return "typescript" }
func (e *TypeScriptExtractor) Extensions() []string { return []string{".ts", ".tsx"} }

func (e *TypeScriptExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "typescript",
	}
	result.Nodes = append(result.Nodes, fileNode)

	// Functions.
	for _, q := range []string{tsQFunction, tsQExport} {
		e.extractFuncs(q, root, src, filePath, fileNode.ID, result)
	}

	// Arrow functions assigned to variables.
	e.extractArrowFuncs(root, src, filePath, fileNode.ID, result)

	// Classes.
	e.extractClasses(root, src, filePath, fileNode.ID, result)

	// Interfaces.
	e.extractInterfaces(root, src, filePath, fileNode.ID, result)

	// Type aliases.
	e.extractTypeAliases(root, src, filePath, fileNode.ID, result)

	// Imports.
	e.extractImports(root, src, filePath, fileNode.ID, result)

	// Call sites.
	e.extractCalls(root, src, filePath, result)

	// Variables.
	e.extractVariables(root, src, filePath, fileNode.ID, result)

	return result, nil
}

func (e *TypeScriptExtractor) extractFuncs(q string, root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult) {
	matches, _ := parser.RunQuery(q, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["func.name"].Text
		def := m.Captures["func.def"]
		id := filePath + "::" + name
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindFunction, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "typescript", Meta: map[string]any{"signature": fmt.Sprintf("function %s()", name)},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}
}

func (e *TypeScriptExtractor) extractArrowFuncs(root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult) {
	matches, _ := parser.RunQuery(tsQArrow, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["func.name"].Text
		def := m.Captures["func.def"]
		id := filePath + "::" + name
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindFunction, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "typescript", Meta: map[string]any{"signature": fmt.Sprintf("const %s = () =>", name)},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}
}

func (e *TypeScriptExtractor) extractClasses(root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult) {
	matches, _ := parser.RunQuery(tsQClass, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["class.name"].Text
		def := m.Captures["class.def"]
		id := filePath + "::" + name
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindType, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "typescript",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})

		// Methods inside the class.
		e.extractMethods(def.Node, src, filePath, id, result)
	}
}

func (e *TypeScriptExtractor) extractMethods(classNode *sitter.Node, src []byte, filePath, classID string, result *parser.ExtractionResult) {
	matches, _ := parser.RunQuery(tsQMethod, e.lang, classNode, src)
	for _, m := range matches {
		name := m.Captures["method.name"].Text
		def := m.Captures["method.def"]
		id := filePath + "::" + classID[strings.LastIndex(classID, "::")+2:] + "." + name
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindMethod, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "typescript",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: id, To: classID, Kind: graph.EdgeMemberOf, FilePath: filePath, Line: def.StartLine + 1,
		})
	}
}

func (e *TypeScriptExtractor) extractInterfaces(root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult) {
	matches, _ := parser.RunQuery(tsQInterface, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["iface.name"].Text
		def := m.Captures["iface.def"]
		id := filePath + "::" + name

		// Walk the interface body to extract method/property signature names.
		var methods []string
		if def.Node != nil {
			methods = extractTSInterfaceMethods(def.Node, src)
		}

		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindInterface, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "typescript",
			Meta:     map[string]any{"methods": methods},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}
}

func (e *TypeScriptExtractor) extractTypeAliases(root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult) {
	matches, _ := parser.RunQuery(tsQTypeAlias, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["type.name"].Text
		def := m.Captures["type.def"]
		id := filePath + "::" + name
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindType, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "typescript",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}
}

func (e *TypeScriptExtractor) extractImports(root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult) {
	matches, _ := parser.RunQuery(tsQImport, e.lang, root, src)
	for _, m := range matches {
		path := m.Captures["import.path"]
		importPath := strings.Trim(path.Text, `"'`)
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: "unresolved::import::" + importPath,
			Kind: graph.EdgeImports, FilePath: filePath, Line: path.StartLine + 1,
		})
	}
}

func (e *TypeScriptExtractor) extractVariables(root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult) {
	matches, _ := parser.RunQuery(tsQVar, e.lang, root, src)

	// Collect names already extracted as arrow functions so we skip them.
	arrowNames := make(map[string]bool)
	for _, n := range result.Nodes {
		if n.Kind == graph.KindFunction && n.FilePath == filePath {
			arrowNames[n.Name] = true
		}
	}

	for _, m := range matches {
		name := m.Captures["var.name"].Text
		def := m.Captures["var.def"]

		// Skip variables already captured as arrow functions.
		if arrowNames[name] {
			continue
		}

		// Only extract module-level variables: the lexical_declaration's parent
		// should be the program (root) node or an export_statement whose parent
		// is the program node.
		parent := def.Node.Parent()
		if parent != nil && parent.Type() == "export_statement" {
			parent = parent.Parent()
		}
		if parent == nil || parent.Type() != "program" {
			continue
		}

		id := filePath + "::" + name
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindVariable, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "typescript",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}
}

// extractTSInterfaceMethods walks children of an interface_declaration node
// to find method_signature and property_signature entries and returns their names.
func extractTSInterfaceMethods(ifaceNode *sitter.Node, src []byte) []string {
	var methods []string
	// Find the interface_body child.
	var body *sitter.Node
	for i := 0; i < int(ifaceNode.NamedChildCount()); i++ {
		child := ifaceNode.NamedChild(i)
		if child.Type() == "interface_body" || child.Type() == "object_type" {
			body = child
			break
		}
	}
	if body == nil {
		return methods
	}

	for i := 0; i < int(body.NamedChildCount()); i++ {
		child := body.NamedChild(i)
		switch child.Type() {
		case "method_signature", "property_signature":
			// The first named child is typically the property_identifier (name).
			for j := 0; j < int(child.NamedChildCount()); j++ {
				nameNode := child.NamedChild(j)
				if nameNode.Type() == "property_identifier" {
					methods = append(methods, nameNode.Content(src))
					break
				}
			}
		}
	}
	return methods
}

func (e *TypeScriptExtractor) extractCalls(root *sitter.Node, src []byte, filePath string, result *parser.ExtractionResult) {
	funcRanges := buildFuncRanges(result)

	matches, _ := parser.RunQuery(tsQCall, e.lang, root, src)
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

	matches, _ = parser.RunQuery(tsQCallMember, e.lang, root, src)
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
