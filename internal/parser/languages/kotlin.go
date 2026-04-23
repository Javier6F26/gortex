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
	kotlinQObject = `(object_declaration
		(type_identifier) @obj.name) @obj.def`

	kotlinQFunction = `(function_declaration
		(simple_identifier) @func.name) @func.def`

	kotlinQClassMethod = `(class_declaration
		(type_identifier) @class.name
		(class_body
			(function_declaration
				(simple_identifier) @method.name) @method.def))`

	kotlinQObjectMethod = `(object_declaration
		(type_identifier) @obj.name
		(class_body
			(function_declaration
				(simple_identifier) @method.name) @method.def))`

	kotlinQImport = `(import_header
		(identifier) @import.path) @import.def`

	kotlinQCall = `(call_expression
		(simple_identifier) @call.name) @call.expr`

	kotlinQCallMember = `(call_expression
		(navigation_expression
			(_) @call.receiver
			(navigation_suffix
				(simple_identifier) @call.method))) @call.expr`

	kotlinQProperty = `(property_declaration
		(variable_declaration
			(simple_identifier) @prop.name)) @prop.def`

	kotlinQPropertyTyped = `(property_declaration
		(variable_declaration
			(simple_identifier) @tprop.name
			(user_type) @tprop.type)) @tprop.def`

)

// KotlinExtractor extracts Kotlin source files.
type KotlinExtractor struct {
	lang *sitter.Language
}

func NewKotlinExtractor() *KotlinExtractor {
	return &KotlinExtractor{lang: grammars.KotlinLanguage()}
}

func (e *KotlinExtractor) Language() string     { return "kotlin" }
func (e *KotlinExtractor) Extensions() []string { return []string{".kt", ".kts"} }

func (e *KotlinExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "kotlin",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)

	// Classes (class, data class).
	// We need to distinguish classes from interfaces. In the Kotlin tree-sitter grammar,
	// both use class_declaration. Interfaces have "interface" as a keyword child.
	// We'll use a manual walk approach for this distinction.
	e.extractClassesAndInterfaces(root, src, filePath, fileNode, result, seen)

	// Object declarations. The odvcencio Kotlin grammar sometimes
	// misparses top-level `object Name { ... }` as an infix_expression
	// whose left operand is `object_literal` (the `object` keyword) and
	// whose lambda body carries the member functions. Recover that case
	// via a manual walk before (and in addition to) the native query so
	// singleton objects still land in the graph.
	e.extractObjects(root, src, filePath, fileNode, result, seen)

	matches, _ := parser.RunQuery(kotlinQObject, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["obj.name"].Text
		def := m.Captures["obj.def"]
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindType, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "kotlin",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Methods inside object declarations.
	matches, _ = parser.RunQuery(kotlinQObjectMethod, e.lang, root, src)
	for _, m := range matches {
		objName := m.Captures["obj.name"].Text
		name := m.Captures["method.name"].Text
		def := m.Captures["method.def"]
		id := filePath + "::" + objName + "." + name
		if seen[id] {
			id = filePath + "::" + objName + "." + name + "_L" + fmt.Sprint(def.StartLine+1)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		seen[filePath+"::_method_L"+fmt.Sprint(def.StartLine+1)] = true
		meta := map[string]any{"receiver": objName}
		if rt := extractKotlinReturnType(def.Node, src, e.lang); rt != "" {
			meta["return_type"] = rt
		}
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindMethod, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "kotlin",
			Meta:     meta,
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
		objID := filePath + "::" + objName
		result.Edges = append(result.Edges, &graph.Edge{
			From: id, To: objID, Kind: graph.EdgeMemberOf, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Methods inside class declarations (already extracted via extractClassesAndInterfaces helper for class membership).
	matches, _ = parser.RunQuery(kotlinQClassMethod, e.lang, root, src)
	for _, m := range matches {
		className := m.Captures["class.name"].Text
		name := m.Captures["method.name"].Text
		def := m.Captures["method.def"]
		id := filePath + "::" + className + "." + name
		if seen[id] {
			id = filePath + "::" + className + "." + name + "_L" + fmt.Sprint(def.StartLine+1)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		seen[filePath+"::_method_L"+fmt.Sprint(def.StartLine+1)] = true
		meta := map[string]any{"receiver": className}
		if rt := extractKotlinReturnType(def.Node, src, e.lang); rt != "" {
			meta["return_type"] = rt
		}
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindMethod, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "kotlin",
			Meta:     meta,
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
		classID := filePath + "::" + className
		result.Edges = append(result.Edges, &graph.Edge{
			From: id, To: classID, Kind: graph.EdgeMemberOf, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Top-level functions (fallback: skip those already found in class/object bodies).
	matches, _ = parser.RunQuery(kotlinQFunction, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["func.name"].Text
		def := m.Captures["func.def"]
		lineKey := filePath + "::_method_L" + fmt.Sprint(def.StartLine+1)
		if seen[lineKey] {
			continue
		}
		id := filePath + "::" + name
		if seen[id] {
			id = filePath + "::" + name + "_L" + fmt.Sprint(def.StartLine+1)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindFunction, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "kotlin",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Top-level properties (val/var not inside a class).
	matches, _ = parser.RunQuery(kotlinQProperty, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["prop.name"].Text
		def := m.Captures["prop.def"]
		// Only include top-level properties (direct children of source_file).
		if def.Node.Parent() != nil && parser.NodeType(def.Node.Parent(), e.lang) == "source_file" {
			id := filePath + "::" + name
			if seen[id] {
				continue
			}
			seen[id] = true
			result.Nodes = append(result.Nodes, &graph.Node{
				ID: id, Kind: graph.KindVariable, Name: name,
				FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
				Language: "kotlin",
			})
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
			})
		}
	}

	// Imports.
	matches, _ = parser.RunQuery(kotlinQImport, e.lang, root, src)
	for _, m := range matches {
		path := m.Captures["import.path"]
		importPath := strings.ReplaceAll(path.Text, ".", "/")
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + importPath,
			Kind: graph.EdgeImports, FilePath: filePath, Line: path.StartLine + 1,
		})
	}

	// Build type environment for receiver type inference.
	tenv := e.buildTypeEnv(root, src)

	// Call sites (with type env).
	e.extractCalls(root, src, filePath, result, tenv)

	return result, nil
}

func (e *KotlinExtractor) extractCalls(root *sitter.Node, src []byte, filePath string, result *parser.ExtractionResult, tenv typeEnv) {
	funcRanges := buildFuncRanges(result)

	// Plain calls: foo()
	matches, _ := parser.RunQuery(kotlinQCall, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["call.name"].Text
		expr := m.Captures["call.expr"]
		callerID := findEnclosingFunc(funcRanges, expr.StartLine+1)
		if callerID == "" {
			continue
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: callerID, To: "unresolved::*." + name,
			Kind: graph.EdgeCalls, FilePath: filePath, Line: expr.StartLine + 1,
		})
	}

	// Member calls: obj.method()
	matches, _ = parser.RunQuery(kotlinQCallMember, e.lang, root, src)
	for _, m := range matches {
		method := m.Captures["call.method"].Text
		receiverText := m.Captures["call.receiver"].Text
		expr := m.Captures["call.expr"]
		callerID := findEnclosingFunc(funcRanges, expr.StartLine+1)
		if callerID == "" {
			continue
		}

		edge := &graph.Edge{
			From: callerID, To: "unresolved::*." + method,
			Kind: graph.EdgeCalls, FilePath: filePath, Line: expr.StartLine + 1,
		}
		if recvType, ok := tenv[receiverText]; ok {
			edge.Meta = map[string]any{"receiver_type": recvType}
		} else if strings.Contains(receiverText, ".") || strings.Contains(receiverText, "(") {
			if chainType := resolveChainType(receiverText, tenv, result); chainType != "" {
				edge.Meta = map[string]any{"receiver_type": chainType}
			}
		}
		result.Edges = append(result.Edges, edge)
	}
}

// buildTypeEnv scans Kotlin property declarations for type annotations (Tier 0)
// and constructor calls (Tier 1: uppercase function call = constructor) to build
// a variable-to-type map.
func (e *KotlinExtractor) buildTypeEnv(root *sitter.Node, src []byte) typeEnv {
	tenv := make(typeEnv)

	// Tier 0: explicit type annotations — val x: Type = ...
	matches, _ := parser.RunQuery(kotlinQPropertyTyped, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["tprop.name"].Text
		typeName := normalizeKotlinTypeName(m.Captures["tprop.type"].Text)
		if typeName != "" {
			tenv[name] = typeName
		}
	}

	// Tier 1: constructor calls — val x = Type(...) (uppercase = class constructor)
	matches, _ = parser.RunQuery(kotlinQProperty, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["prop.name"].Text
		if _, exists := tenv[name]; exists {
			continue
		}
		defNode := m.Captures["prop.def"].Node
		if defNode == nil {
			continue
		}
		// Walk the property declaration looking for call_expression with uppercase identifier.
		walkNodes(defNode, func(n *sitter.Node) {
			if parser.NodeType(n, e.lang) == "call_expression" {
				// First child should be the function name (simple_identifier).
				if n.NamedChildCount() > 0 {
					nameNode := n.NamedChild(0)
					if parser.NodeType(nameNode, e.lang) == "simple_identifier" {
						funcName := nameNode.Text(src)
						if len(funcName) > 0 && funcName[0] >= 'A' && funcName[0] <= 'Z' {
							tenv[name] = funcName
						}
					}
				}
			}
		})
	}

	return tenv
}

// normalizeKotlinTypeName strips generics and nullable markers from a Kotlin type name.
func normalizeKotlinTypeName(t string) string {
	t = strings.TrimSpace(t)
	// Remove nullable suffix.
	t = strings.TrimSuffix(t, "?")
	// Remove generics.
	if idx := strings.Index(t, "<"); idx > 0 {
		t = t[:idx]
	}
	// Skip Kotlin primitives.
	switch t {
	case "Int", "Long", "Short", "Byte", "Float", "Double", "Boolean",
		"Char", "String", "Unit", "Any", "Nothing":
		return ""
	}
	if t == "" || (t[0] >= 'a' && t[0] <= 'z') {
		return ""
	}
	return t
}

// extractClassesAndInterfaces walks the root to distinguish class_declaration
// nodes that are interfaces vs classes. In the Kotlin tree-sitter grammar,
// both classes and interfaces use class_declaration, but interfaces have
// the "interface" keyword as the first child token.
func (e *KotlinExtractor) extractClassesAndInterfaces(
	root *sitter.Node, src []byte, filePath string, fileNode *graph.Node,
	result *parser.ExtractionResult, seen map[string]bool,
) {
	walkNodes(root, func(node *sitter.Node) {
		if parser.NodeType(node, e.lang) != "class_declaration" {
			return
		}

		// Find the type_identifier child for the name.
		var name string
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			if parser.NodeType(child, e.lang) == "type_identifier" {
				name = child.Text(src)
				break
			}
		}
		if name == "" {
			return
		}

		id := filePath + "::" + name
		if seen[id] {
			return
		}

		// Determine if this is an interface by checking for the
		// "interface" keyword, or an enum by locating an
		// enum_class_body as the body (only enums have one; regular
		// classes and interfaces use class_body).
		isInterface := false
		var enumBody *sitter.Node
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(i)
			switch parser.NodeType(child, e.lang) {
			case "interface":
				isInterface = true
			case "enum_class_body":
				enumBody = child
			}
		}

		kind := graph.KindType
		meta := map[string]any(nil)
		if isInterface {
			kind = graph.KindInterface
		} else if enumBody != nil {
			meta = map[string]any{"kind": "enum"}
		}

		seen[id] = true
		startLine := int(node.StartPoint().Row) + 1
		endLine := int(node.EndPoint().Row) + 1
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: kind, Name: name,
			FilePath: filePath, StartLine: startLine, EndLine: endLine,
			Language: "kotlin",
			Meta:     meta,
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: startLine,
		})

		// Enum entries — `NORTH, SOUTH, EAST, WEST` — walk the
		// enum_class_body's enum_entry children. Each enum_entry has
		// a single simple_identifier naming the case.
		if enumBody != nil {
			for i := 0; i < int(enumBody.ChildCount()); i++ {
				entry := enumBody.Child(i)
				if entry == nil || parser.NodeType(entry, e.lang) != "enum_entry" {
					continue
				}
				var entryName string
				for j := 0; j < int(entry.ChildCount()); j++ {
					ch := entry.Child(j)
					if ch != nil && parser.NodeType(ch, e.lang) == "simple_identifier" {
						entryName = ch.Text(src)
						break
					}
				}
				if entryName == "" {
					continue
				}
				entryID := id + "." + entryName
				result.Nodes = append(result.Nodes, &graph.Node{
					ID: entryID, Kind: graph.KindVariable, Name: entryName,
					FilePath:  filePath,
					StartLine: int(entry.StartPoint().Row) + 1,
					EndLine:   int(entry.EndPoint().Row) + 1,
					Language:  "kotlin",
					Meta:      map[string]any{"receiver": name, "kind": "enum_entry"},
				})
				result.Edges = append(result.Edges, &graph.Edge{
					From: entryID, To: id, Kind: graph.EdgeMemberOf,
					FilePath: filePath, Line: int(entry.StartPoint().Row) + 1,
				})
			}
		}
	})
}

// extractObjects walks the AST for Kotlin `object Name { ... }` declarations
// that the odvcencio grammar misparses as an `infix_expression` whose left
// operand is the `object` keyword (as `object_literal`). When that shape is
// detected, emit the same node + method set the native object_declaration
// path produces so Singleton-style objects still index correctly.
func (e *KotlinExtractor) extractObjects(
	root *sitter.Node, src []byte, filePath string, fileNode *graph.Node,
	result *parser.ExtractionResult, seen map[string]bool,
) {
	walkNodes(root, func(node *sitter.Node) {
		if parser.NodeType(node, e.lang) != "infix_expression" {
			return
		}
		// Expect: object_literal, simple_identifier, lambda_literal.
		var nameNode, body *sitter.Node
		sawObjLit := false
		for i := 0; i < int(node.ChildCount()); i++ {
			c := node.Child(i)
			if c == nil {
				continue
			}
			switch parser.NodeType(c, e.lang) {
			case "object_literal":
				if strings.TrimSpace(c.Text(src)) == "object" {
					sawObjLit = true
				}
			case "simple_identifier":
				if sawObjLit && nameNode == nil {
					nameNode = c
				}
			case "lambda_literal":
				if sawObjLit && body == nil {
					body = c
				}
			}
		}
		if !sawObjLit || nameNode == nil {
			return
		}

		name := nameNode.Text(src)
		id := filePath + "::" + name
		if !seen[id] {
			seen[id] = true
			startLine := int(node.StartPoint().Row) + 1
			endLine := int(node.EndPoint().Row) + 1
			result.Nodes = append(result.Nodes, &graph.Node{
				ID: id, Kind: graph.KindType, Name: name,
				FilePath: filePath, StartLine: startLine, EndLine: endLine,
				Language: "kotlin",
			})
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: startLine,
			})
		}

		if body == nil {
			return
		}
		// Pick up function_declaration children as methods on this object.
		walkNodes(body, func(inner *sitter.Node) {
			if parser.NodeType(inner, e.lang) != "function_declaration" {
				return
			}
			var methodNameNode *sitter.Node
			for i := 0; i < int(inner.ChildCount()); i++ {
				c := inner.Child(i)
				if c != nil && parser.NodeType(c, e.lang) == "simple_identifier" {
					methodNameNode = c
					break
				}
			}
			if methodNameNode == nil {
				return
			}
			mName := methodNameNode.Text(src)
			startLine := int(inner.StartPoint().Row) + 1
			methodID := filePath + "::" + name + "." + mName
			if seen[methodID] {
				methodID = filePath + "::" + name + "." + mName + "_L" + fmt.Sprint(startLine)
			}
			if seen[methodID] {
				return
			}
			seen[methodID] = true
			seen[filePath+"::_method_L"+fmt.Sprint(startLine)] = true

			meta := map[string]any{"receiver": name}
			if rt := extractKotlinReturnType(inner, src, e.lang); rt != "" {
				meta["return_type"] = rt
			}
			result.Nodes = append(result.Nodes, &graph.Node{
				ID: methodID, Kind: graph.KindMethod, Name: mName,
				FilePath: filePath, StartLine: startLine, EndLine: int(inner.EndPoint().Row) + 1,
				Language: "kotlin",
				Meta:     meta,
			})
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: methodID, Kind: graph.EdgeDefines, FilePath: filePath, Line: startLine,
			})
			result.Edges = append(result.Edges, &graph.Edge{
				From: methodID, To: id, Kind: graph.EdgeMemberOf, FilePath: filePath, Line: startLine,
			})
		})
	})
}

// extractKotlinReturnType walks a function_declaration node to find the return type annotation.
// Kotlin functions have optional `: ReturnType` after the parameter list.
func extractKotlinReturnType(node *sitter.Node, src []byte, lang *sitter.Language) string {
	if node == nil {
		return ""
	}
	// Look for user_type or nullable_type child after the function_value_parameters.
	pastParams := false
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if parser.NodeType(child, lang) == "function_value_parameters" {
			pastParams = true
			continue
		}
		if pastParams {
			switch parser.NodeType(child, lang) {
			case "user_type", "nullable_type":
				rawType := string(src[child.StartByte():child.EndByte()])
				if rt := normalizeKotlinTypeName(rawType); rt != "" {
					return rt
				}
			case "function_body":
				// Stop looking once we hit the body.
				return ""
			}
		}
	}
	return ""
}

// walkNodes does a depth-first walk of the tree-sitter node tree.
func walkNodes(node *sitter.Node, fn func(*sitter.Node)) {
	fn(node)
	for i := 0; i < int(node.ChildCount()); i++ {
		walkNodes(node.Child(i), fn)
	}
}
