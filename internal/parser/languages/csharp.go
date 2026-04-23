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
	csharpQClass = `(class_declaration
		name: (identifier) @class.name) @class.def`

	csharpQInterface = `(interface_declaration
		name: (identifier) @iface.name) @iface.def`

	csharpQStruct = `(struct_declaration
		name: (identifier) @struct.name) @struct.def`

	csharpQEnum = `(enum_declaration
		name: (identifier) @enum.name) @enum.def`

	csharpQNamespace = `(namespace_declaration
		name: (_) @ns.name) @ns.def`

	csharpQUsing = `(using_directive (_) @using.path) @using.def`

	csharpQClassMethod = `(class_declaration
		name: (identifier) @class.name
		body: (declaration_list
			(method_declaration
				name: (identifier) @method.name) @method.def))`

	csharpQStructMethod = `(struct_declaration
		name: (identifier) @struct.name
		body: (declaration_list
			(method_declaration
				name: (identifier) @method.name) @method.def))`

	csharpQClassConstructor = `(class_declaration
		name: (identifier) @class.name
		body: (declaration_list
			(constructor_declaration
				name: (identifier) @ctor.name) @ctor.def))`

	csharpQStructConstructor = `(struct_declaration
		name: (identifier) @struct.name
		body: (declaration_list
			(constructor_declaration
				name: (identifier) @ctor.name) @ctor.def))`

	csharpQClassField = `(class_declaration
		name: (identifier) @class.name
		body: (declaration_list
			(field_declaration
				(variable_declaration
					(variable_declarator
						name: (identifier) @field.name))) @field.def))`

	csharpQStructField = `(struct_declaration
		name: (identifier) @struct.name
		body: (declaration_list
			(field_declaration
				(variable_declaration
					(variable_declarator
						name: (identifier) @field.name))) @field.def))`

	csharpQClassProperty = `(class_declaration
		name: (identifier) @class.name
		body: (declaration_list
			(property_declaration
				name: (identifier) @prop.name) @prop.def))`

	csharpQStructProperty = `(struct_declaration
		name: (identifier) @struct.name
		body: (declaration_list
			(property_declaration
				name: (identifier) @prop.name) @prop.def))`

	csharpQIfaceMethod = `(interface_declaration
		name: (identifier) @iface.name
		body: (declaration_list
			(method_declaration
				name: (identifier) @iface.method.name)))`

	csharpQCall = `(invocation_expression
		function: (identifier) @call.name) @call.expr`

	csharpQCallMember = `(invocation_expression
		function: (member_access_expression
			expression: (_) @call.receiver
			name: (identifier) @call.method)) @call.expr`

	// Tier 0: explicit type — UserService svc = ...
	csharpQLocalTyped = `(local_declaration_statement
		(variable_declaration
			type: (_) @lvar.type
			(variable_declarator
				(identifier) @lvar.name))) @lvar.def`

	// For Tier 1 (new): we walk variable_declarator nodes in buildTypeEnv.
)

// CSharpExtractor extracts C# source files.
type CSharpExtractor struct {
	lang *sitter.Language
}

func NewCSharpExtractor() *CSharpExtractor {
	return &CSharpExtractor{lang: grammars.CSharpLanguage()}
}

func (e *CSharpExtractor) Language() string     { return "csharp" }
func (e *CSharpExtractor) Extensions() []string { return []string{".cs"} }

func (e *CSharpExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "csharp",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)

	// Namespaces.
	matches, _ := parser.RunQuery(csharpQNamespace, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["ns.name"].Text
		def := m.Captures["ns.def"]
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindPackage, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "csharp",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Classes.
	matches, _ = parser.RunQuery(csharpQClass, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["class.name"].Text
		def := m.Captures["class.def"]
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindType, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "csharp",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Interfaces.
	matches, _ = parser.RunQuery(csharpQInterface, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["iface.name"].Text
		def := m.Captures["iface.def"]
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindInterface, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "csharp",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Interface method names into Meta["methods"].
	ifaceMethodMatches, _ := parser.RunQuery(csharpQIfaceMethod, e.lang, root, src)
	ifaceMethods := make(map[string][]string)
	for _, m := range ifaceMethodMatches {
		ifaceName := m.Captures["iface.name"].Text
		methodName := m.Captures["iface.method.name"].Text
		ifaceMethods[ifaceName] = append(ifaceMethods[ifaceName], methodName)
	}
	for _, n := range result.Nodes {
		if n.Kind == graph.KindInterface {
			if methods, ok := ifaceMethods[n.Name]; ok {
				if n.Meta == nil {
					n.Meta = make(map[string]any)
				}
				n.Meta["methods"] = methods
			}
		}
	}

	// Structs.
	matches, _ = parser.RunQuery(csharpQStruct, e.lang, root, src)
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
			Language: "csharp",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Enums.
	matches, _ = parser.RunQuery(csharpQEnum, e.lang, root, src)
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
			Language: "csharp",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Methods in classes.
	e.extractMethods(filePath, src, root, result, seen, csharpQClassMethod, "class.name")

	// Methods in structs.
	e.extractMethods(filePath, src, root, result, seen, csharpQStructMethod, "struct.name")

	// Constructors in classes.
	e.extractConstructors(filePath, src, root, result, seen, csharpQClassConstructor, "class.name")

	// Constructors in structs.
	e.extractConstructors(filePath, src, root, result, seen, csharpQStructConstructor, "struct.name")

	// Fields in classes.
	e.extractFields(filePath, src, root, result, seen, csharpQClassField, "class.name", "field")

	// Fields in structs.
	e.extractFields(filePath, src, root, result, seen, csharpQStructField, "struct.name", "field")

	// Properties in classes.
	e.extractFields(filePath, src, root, result, seen, csharpQClassProperty, "class.name", "prop")

	// Properties in structs.
	e.extractFields(filePath, src, root, result, seen, csharpQStructProperty, "struct.name", "prop")

	// Using directives.
	matches, _ = parser.RunQuery(csharpQUsing, e.lang, root, src)
	for _, m := range matches {
		path := m.Captures["using.path"]
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

func (e *CSharpExtractor) extractCalls(root *sitter.Node, src []byte, filePath string, result *parser.ExtractionResult, tenv typeEnv) {
	funcRanges := buildFuncRanges(result)

	// Plain function calls: Foo()
	matches, _ := parser.RunQuery(csharpQCall, e.lang, root, src)
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

	// Member calls: obj.Method()
	matches, _ = parser.RunQuery(csharpQCallMember, e.lang, root, src)
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

// buildTypeEnv scans C# local variable declarations for type annotations (Tier 0)
// and new/object_creation expressions (Tier 1) to build a variable-to-type map.
func (e *CSharpExtractor) buildTypeEnv(root *sitter.Node, src []byte) typeEnv {
	tenv := make(typeEnv)

	// Tier 0: explicit type annotations — UserService svc = ...
	matches, _ := parser.RunQuery(csharpQLocalTyped, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["lvar.name"].Text
		typeName := normalizeCSharpTypeName(m.Captures["lvar.type"].Text)
		if typeName != "" && typeName != "var" {
			tenv[name] = typeName
		}
	}

	// Tier 1: var svc = new UserService() — walk for object_creation_expression
	// Re-scan the typed locals for "var" declarations and check RHS for new.
	for _, m := range matches {
		name := m.Captures["lvar.name"].Text
		if _, exists := tenv[name]; exists {
			continue
		}
		typeText := m.Captures["lvar.type"].Text
		if typeText != "var" {
			continue
		}
		defNode := m.Captures["lvar.def"].Node
		if defNode == nil {
			continue
		}
		walkNodes(defNode, func(n *sitter.Node) {
			if parser.NodeType(n, e.lang) == "object_creation_expression" {
				typeName := inferTypeFromCSharpNew(n, src, e.lang)
				if typeName != "" {
					tenv[name] = typeName
				}
			}
		})
	}

	return tenv
}

// normalizeCSharpTypeName strips generics and nullable markers from a C# type name.
func normalizeCSharpTypeName(t string) string {
	t = strings.TrimSpace(t)
	// Remove nullable suffix.
	t = strings.TrimSuffix(t, "?")
	// Remove array suffix.
	if idx := strings.Index(t, "["); idx > 0 {
		t = t[:idx]
	}
	// Remove generics.
	if idx := strings.Index(t, "<"); idx > 0 {
		t = t[:idx]
	}
	// Skip C# primitives and keywords.
	switch t {
	case "var", "int", "long", "short", "byte", "float", "double", "decimal",
		"bool", "char", "string", "object", "void", "dynamic":
		if t == "var" {
			return "var" // caller handles this specially
		}
		return ""
	}
	if t == "" || (t[0] >= 'a' && t[0] <= 'z') {
		return ""
	}
	return t
}

// inferTypeFromCSharpNew extracts the type name from a C# object_creation_expression.
// new UserService(...) -> "UserService"
func inferTypeFromCSharpNew(node *sitter.Node, src []byte, lang *sitter.Language) string {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		childType := parser.NodeType(child, lang)
		if childType == "identifier" || childType == "type_identifier" ||
			childType == "generic_name" || childType == "qualified_name" {
			name := child.Text(src)
			// Strip generics from generic_name.
			if idx := strings.Index(name, "<"); idx > 0 {
				name = name[:idx]
			}
			if len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z' {
				return name
			}
		}
	}
	return ""
}

func (e *CSharpExtractor) extractMethods(
	filePath string, src []byte, root *sitter.Node,
	result *parser.ExtractionResult, seen map[string]bool,
	query string, ownerCapture string,
) {
	matches, _ := parser.RunQuery(query, e.lang, root, src)
	for _, m := range matches {
		ownerName := m.Captures[ownerCapture].Text
		name := m.Captures["method.name"].Text
		def := m.Captures["method.def"]
		id := filePath + "::" + ownerName + "." + name
		if seen[id] {
			id = filePath + "::" + ownerName + "." + name + "_L" + fmt.Sprint(def.StartLine+1)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		meta := map[string]any{"receiver": ownerName}
		// Extract return type from method_declaration node.
		// In C# tree-sitter, method_declaration has a type child before the name.
		if def.Node != nil {
			for i := 0; i < int(def.Node.ChildCount()); i++ {
				child := def.Node.Child(i)
				if parser.NodeType(child, e.lang) == "identifier" && string(src[child.StartByte():child.EndByte()]) == name {
					break
				}
				childType := parser.NodeType(child, e.lang)
				// Type nodes include predefined_type, identifier, qualified_name, generic_name, nullable_type, array_type, etc.
				switch childType {
				case "predefined_type", "identifier", "qualified_name", "generic_name",
					"nullable_type", "array_type", "tuple_type":
					rawType := string(src[child.StartByte():child.EndByte()])
					if rt := normalizeCSharpTypeName(rawType); rt != "" && rt != "var" {
						meta["return_type"] = rt
					}
				}
			}
		}
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindMethod, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "csharp",
			Meta:     meta,
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: filePath, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
		ownerID := filePath + "::" + ownerName
		result.Edges = append(result.Edges, &graph.Edge{
			From: id, To: ownerID, Kind: graph.EdgeMemberOf, FilePath: filePath, Line: def.StartLine + 1,
		})
	}
}

func (e *CSharpExtractor) extractConstructors(
	filePath string, src []byte, root *sitter.Node,
	result *parser.ExtractionResult, seen map[string]bool,
	query string, ownerCapture string,
) {
	matches, _ := parser.RunQuery(query, e.lang, root, src)
	for _, m := range matches {
		ownerName := m.Captures[ownerCapture].Text
		def := m.Captures["ctor.def"]
		id := filePath + "::" + ownerName + ".<init>"
		if seen[id] {
			id = filePath + "::" + ownerName + ".<init>_L" + fmt.Sprint(def.StartLine+1)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindMethod, Name: ownerName + ".<init>",
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "csharp",
			Meta:     map[string]any{"receiver": ownerName},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: filePath, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
		ownerID := filePath + "::" + ownerName
		result.Edges = append(result.Edges, &graph.Edge{
			From: id, To: ownerID, Kind: graph.EdgeMemberOf, FilePath: filePath, Line: def.StartLine + 1,
		})
	}
}

func (e *CSharpExtractor) extractFields(
	filePath string, src []byte, root *sitter.Node,
	result *parser.ExtractionResult, seen map[string]bool,
	query string, ownerCapture string, fieldCapture string,
) {
	matches, _ := parser.RunQuery(query, e.lang, root, src)
	for _, m := range matches {
		ownerName := m.Captures[ownerCapture].Text
		name := m.Captures[fieldCapture+".name"].Text
		def := m.Captures[fieldCapture+".def"]
		id := filePath + "::" + ownerName + "." + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindVariable, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "csharp",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: filePath, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
		ownerID := filePath + "::" + ownerName
		result.Edges = append(result.Edges, &graph.Edge{
			From: id, To: ownerID, Kind: graph.EdgeMemberOf, FilePath: filePath, Line: def.StartLine + 1,
		})
	}
}
