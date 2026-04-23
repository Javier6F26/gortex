package languages

import (
	"fmt"
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// Tree-sitter query patterns for C++ source files.
const (
	qCppFunction = `(function_definition
		declarator: (function_declarator
			declarator: (identifier) @func.name)) @func.def`

	qCppClass = `(class_specifier
		name: (type_identifier) @class.name) @class.def`

	qCppStruct = `(struct_specifier
		name: (type_identifier) @struct.name) @struct.def`

	qCppEnum = `(enum_specifier
		name: (type_identifier) @enum.name) @enum.def`

	qCppInclude = `(preproc_include
		path: (_) @include.path) @include.def`

	qCppCall = `(call_expression
		function: (identifier) @call.name) @call.expr`

	qCppMethodCall = `(call_expression
		function: (field_expression
			field: (field_identifier) @call.method)) @call.expr`

	qCppNamespace = `(namespace_definition
		name: (namespace_identifier) @ns.name) @ns.def`
)

// CppExtractor extracts C++ source files into graph nodes and edges.
type CppExtractor struct {
	lang *sitter.Language
}

func NewCppExtractor() *CppExtractor {
	return &CppExtractor{lang: grammars.CppLanguage()}
}

func (e *CppExtractor) Language() string     { return "cpp" }
func (e *CppExtractor) Extensions() []string { return []string{".cpp", ".cc", ".cxx", ".hpp"} }

func (e *CppExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "cpp",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)

	// Namespaces.
	e.extractNamespaces(root, src, filePath, fileNode.ID, result)

	// Classes (with methods via manual walking).
	e.extractClasses(root, src, filePath, fileNode.ID, seen, result)

	// Structs.
	e.extractStructs(root, src, filePath, fileNode.ID, seen, result)

	// Enums.
	e.extractEnums(root, src, filePath, fileNode.ID, seen, result)

	// Free functions (not inside classes).
	e.extractFunctions(root, src, filePath, fileNode.ID, seen, result)

	// Includes.
	e.extractIncludes(root, src, filePath, fileNode.ID, result)

	// Call sites.
	e.extractCalls(root, src, filePath, result)

	return result, nil
}

func (e *CppExtractor) extractNamespaces(root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult) {
	matches, err := parser.RunQuery(qCppNamespace, e.lang, root, src)
	if err != nil {
		return
	}
	for _, m := range matches {
		name := m.Captures["ns.name"].Text
		def := m.Captures["ns.def"]
		id := filePath + "::" + name
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindPackage, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "cpp",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}
}

func (e *CppExtractor) extractClasses(root *sitter.Node, src []byte, filePath, fileID string, seen map[string]bool, result *parser.ExtractionResult) {
	matches, err := parser.RunQuery(qCppClass, e.lang, root, src)
	if err != nil {
		return
	}
	for _, m := range matches {
		className := m.Captures["class.name"].Text
		def := m.Captures["class.def"]
		classID := filePath + "::" + className
		if seen[classID] {
			continue
		}
		seen[classID] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: classID, Kind: graph.KindType, Name: className,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "cpp",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: classID, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})

		// Walk the class body to find methods.
		e.extractClassMethods(def.Node, src, filePath, fileID, className, classID, seen, result)
	}
}

func (e *CppExtractor) extractClassMethods(classNode *sitter.Node, src []byte, filePath, fileID, className, classID string, seen map[string]bool, result *parser.ExtractionResult) {
	// Find the field_declaration_list (class body).
	var body *sitter.Node
	for i := 0; i < int(classNode.NamedChildCount()); i++ {
		child := classNode.NamedChild(i)
		if parser.NodeType(child, e.lang) == "field_declaration_list" {
			body = child
			break
		}
	}
	if body == nil {
		return
	}

	// Walk the body looking for function_definition nodes.
	for i := 0; i < int(body.NamedChildCount()); i++ {
		child := body.NamedChild(i)
		// Handle access specifiers that wrap declarations.
		if parser.NodeType(child, e.lang) == "access_specifier" {
			continue
		}
		if parser.NodeType(child, e.lang) == "function_definition" {
			e.addMethodFromNode(child, src, filePath, fileID, className, classID, seen, result)
		}
		// Also check inside declaration_list (e.g. under access specifiers).
		if parser.NodeType(child, e.lang) == "declaration_list" {
			for j := 0; j < int(child.NamedChildCount()); j++ {
				gc := child.NamedChild(j)
				if parser.NodeType(gc, e.lang) == "function_definition" {
					e.addMethodFromNode(gc, src, filePath, fileID, className, classID, seen, result)
				}
			}
		}
	}
}

func (e *CppExtractor) addMethodFromNode(funcNode *sitter.Node, src []byte, filePath, fileID, className, classID string, seen map[string]bool, result *parser.ExtractionResult) {
	// Extract method name from function_definition -> declarator -> declarator.
	methodName := extractFuncName(funcNode, src, e.lang)
	if methodName == "" {
		return
	}
	startLine := int(funcNode.StartPoint().Row) + 1
	endLine := int(funcNode.EndPoint().Row) + 1

	id := filePath + "::" + className + "." + methodName
	if seen[id] {
		id = filePath + "::" + className + "." + methodName + "_L" + fmt.Sprint(startLine)
	}
	if seen[id] {
		return
	}
	seen[id] = true
	// Mark line so free function extraction skips this.
	seen[filePath+"::_method_L"+fmt.Sprint(startLine)] = true

	result.Nodes = append(result.Nodes, &graph.Node{
		ID: id, Kind: graph.KindMethod, Name: methodName,
		FilePath: filePath, StartLine: startLine, EndLine: endLine,
		Language: "cpp",
		Meta:     map[string]any{"receiver": className},
	})
	result.Edges = append(result.Edges, &graph.Edge{
		From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: startLine,
	})
	result.Edges = append(result.Edges, &graph.Edge{
		From: id, To: classID, Kind: graph.EdgeMemberOf, FilePath: filePath, Line: startLine,
	})
}

// extractFuncName walks a function_definition node to find the function name.
// It handles both `identifier` (free functions) and `field_identifier` (methods).
func extractFuncName(funcNode *sitter.Node, src []byte, lang *sitter.Language) string {
	// function_definition -> declarator (function_declarator) -> declarator (identifier or field_identifier)
	for i := 0; i < int(funcNode.NamedChildCount()); i++ {
		child := funcNode.NamedChild(i)
		if parser.NodeType(child, lang) == "function_declarator" {
			for j := 0; j < int(child.NamedChildCount()); j++ {
				gc := child.NamedChild(j)
				switch parser.NodeType(gc, lang) {
				case "identifier", "field_identifier", "destructor_name":
					return gc.Text(src)
				case "qualified_identifier":
					// e.g. ClassName::methodName — extract last part.
					return lastIdentifier(gc, src, lang)
				}
			}
		}
	}
	return ""
}

// lastIdentifier extracts the last identifier from a qualified_identifier.
func lastIdentifier(node *sitter.Node, src []byte, lang *sitter.Language) string {
	name := ""
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		switch parser.NodeType(child, lang) {
		case "identifier", "field_identifier", "destructor_name":
			name = child.Text(src)
		}
	}
	return name
}

func (e *CppExtractor) extractStructs(root *sitter.Node, src []byte, filePath, fileID string, seen map[string]bool, result *parser.ExtractionResult) {
	matches, err := parser.RunQuery(qCppStruct, e.lang, root, src)
	if err != nil {
		return
	}
	for _, m := range matches {
		name := m.Captures["struct.name"].Text
		def := m.Captures["struct.def"]
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindType, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "cpp",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}
}

func (e *CppExtractor) extractEnums(root *sitter.Node, src []byte, filePath, fileID string, seen map[string]bool, result *parser.ExtractionResult) {
	matches, err := parser.RunQuery(qCppEnum, e.lang, root, src)
	if err != nil {
		return
	}
	for _, m := range matches {
		name := m.Captures["enum.name"].Text
		def := m.Captures["enum.def"]
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindType, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "cpp",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}
}

func (e *CppExtractor) extractFunctions(root *sitter.Node, src []byte, filePath, fileID string, seen map[string]bool, result *parser.ExtractionResult) {
	matches, err := parser.RunQuery(qCppFunction, e.lang, root, src)
	if err != nil {
		return
	}
	for _, m := range matches {
		name := m.Captures["func.name"].Text
		def := m.Captures["func.def"]
		startLine := def.StartLine + 1
		// Skip methods already extracted from class bodies.
		lineKey := filePath + "::_method_L" + fmt.Sprint(startLine)
		if seen[lineKey] {
			continue
		}
		id := filePath + "::" + name
		if seen[id] {
			id = filePath + "::" + name + "_L" + fmt.Sprint(startLine)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindFunction, Name: name,
			FilePath: filePath, StartLine: startLine, EndLine: def.EndLine + 1,
			Language: "cpp",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: startLine,
		})
	}
}

func (e *CppExtractor) extractIncludes(root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult) {
	matches, err := parser.RunQuery(qCppInclude, e.lang, root, src)
	if err != nil {
		return
	}
	for _, m := range matches {
		pathCap := m.Captures["include.path"]
		// Strip quotes and angle brackets.
		includePath := pathCap.Text
		includePath = strings.Trim(includePath, `"<>`)
		result.Edges = append(result.Edges, &graph.Edge{
			From:     fileID,
			To:       "unresolved::import::" + includePath,
			Kind:     graph.EdgeImports,
			FilePath: filePath,
			Line:     pathCap.StartLine + 1,
		})
	}
}

func (e *CppExtractor) extractCalls(root *sitter.Node, src []byte, filePath string, result *parser.ExtractionResult) {
	funcRanges := buildFuncRanges(result)

	// Plain function calls: foo()
	matches, _ := parser.RunQuery(qCppCall, e.lang, root, src)
	for _, m := range matches {
		callName := m.Captures["call.name"].Text
		expr := m.Captures["call.expr"]
		callerID := findEnclosingFunc(funcRanges, expr.StartLine+1)
		if callerID == "" {
			continue
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From:     callerID,
			To:       "unresolved::" + callName,
			Kind:     graph.EdgeCalls,
			FilePath: filePath,
			Line:     expr.StartLine + 1,
		})
	}

	// Method calls: obj.method()
	matches, _ = parser.RunQuery(qCppMethodCall, e.lang, root, src)
	for _, m := range matches {
		methodName := m.Captures["call.method"].Text
		expr := m.Captures["call.expr"]
		callerID := findEnclosingFunc(funcRanges, expr.StartLine+1)
		if callerID == "" {
			continue
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From:     callerID,
			To:       "unresolved::*." + methodName,
			Kind:     graph.EdgeCalls,
			FilePath: filePath,
			Line:     expr.StartLine + 1,
		})
	}
}
