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
	javaQClass = `(class_declaration
		name: (identifier) @class.name) @class.def`

	javaQInterface = `(interface_declaration
		name: (identifier) @iface.name) @iface.def`

	javaQMethod = `(method_declaration
		name: (identifier) @method.name) @method.def`

	javaQClassMethod = `(class_declaration
		name: (identifier) @class.name
		body: (class_body
			(method_declaration
				name: (identifier) @method.name) @method.def))`

	javaQConstructor = `(constructor_declaration
		name: (identifier) @ctor.name) @ctor.def`

	javaQClassConstructor = `(class_declaration
		name: (identifier) @class.name
		body: (class_body
			(constructor_declaration
				name: (identifier) @ctor.name) @ctor.def))`

	javaQImport = `(import_declaration
		(scoped_identifier) @import.path) @import.def`

	javaQCall = `(method_invocation
		name: (identifier) @call.name) @call.expr`

	javaQCallMember = `(method_invocation
		object: (_) @call.receiver
		name: (identifier) @call.method) @call.expr`

	javaQClassField = `(class_declaration
		name: (identifier) @class.name
		body: (class_body
			(field_declaration
				declarator: (variable_declarator
					name: (identifier) @field.name)) @field.def))`

	javaQIfaceMethod = `(interface_declaration
		name: (identifier) @iface.name
		body: (interface_body
			(method_declaration
				name: (identifier) @iface.method.name)))`

	// Tier 0: explicit local variable declarations — Type varName = ...
	javaQLocalVar = `(local_variable_declaration
		type: (_) @lvar.type
		declarator: (variable_declarator
			name: (identifier) @lvar.name)) @lvar.def`

	// Tier 0: explicit field declarations — Type fieldName = ...
	javaQFieldVar = `(field_declaration
		type: (_) @fvar.type
		declarator: (variable_declarator
			name: (identifier) @fvar.name)) @fvar.def`

	// Enums. Java enums are first-class classes with members — a
	// typical codebase has dozens of them (Status, Role, EventType,
	// etc.) and they're common navigation targets. Skipping them
	// leaves enterprise Java graphs materially incomplete.
	javaQEnum = `(enum_declaration
		name: (identifier) @enum.name) @enum.def`

	// Enum constants (members) — the right-hand-side values inside
	// an enum body: `ACTIVE, INACTIVE, PENDING(5)` etc.
	javaQEnumConstant = `(enum_declaration
		name: (identifier) @enum.name
		body: (enum_body
			(enum_constant
				name: (identifier) @enum.member) @enum.member.def))`
)

// JavaExtractor extracts Java source files.
type JavaExtractor struct {
	lang *sitter.Language
}

func NewJavaExtractor() *JavaExtractor {
	return &JavaExtractor{lang: grammars.JavaLanguage()}
}

func (e *JavaExtractor) Language() string     { return "java" }
func (e *JavaExtractor) Extensions() []string { return []string{".java"} }

func (e *JavaExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "java",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)

	// Classes.
	matches, _ := parser.RunQuery(javaQClass, e.lang, root, src)
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
			Language: "java",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Enums — declaration + each constant as a member. KindType
	// matches how classes are stored; Meta carries "kind":"enum" so
	// downstream consumers can still distinguish enums from classes.
	matches, _ = parser.RunQuery(javaQEnum, e.lang, root, src)
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
			Language: "java",
			Meta:     map[string]any{"kind": "enum"},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Enum constants (members).
	memberMatches, _ := parser.RunQuery(javaQEnumConstant, e.lang, root, src)
	for _, m := range memberMatches {
		enumName := m.Captures["enum.name"].Text
		memberName := m.Captures["enum.member"].Text
		memberDef := m.Captures["enum.member.def"]
		enumID := filePath + "::" + enumName
		memberID := enumID + "." + memberName
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: memberID, Kind: graph.KindVariable, Name: memberName,
			FilePath:  filePath,
			StartLine: memberDef.StartLine + 1,
			EndLine:   memberDef.EndLine + 1,
			Language:  "java",
			Meta:      map[string]any{"receiver": enumName, "kind": "enum_member"},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: memberID, To: enumID, Kind: graph.EdgeMemberOf,
			FilePath: filePath, Line: memberDef.StartLine + 1,
		})
	}

	// Interfaces.
	matches, _ = parser.RunQuery(javaQInterface, e.lang, root, src)
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
			Language: "java",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Extract interface method names into Meta["methods"] for IMPLEMENTS inference.
	ifaceMethodMatches, _ := parser.RunQuery(javaQIfaceMethod, e.lang, root, src)
	ifaceMethods := make(map[string][]string)
	for _, m := range ifaceMethodMatches {
		ifaceName := m.Captures["iface.name"].Text
		methodName := m.Captures["iface.method.name"].Text
		ifaceMethods[ifaceName] = append(ifaceMethods[ifaceName], methodName)
	}
	for _, n := range result.Nodes {
		if n.Kind == graph.KindInterface {
			name := n.Name
			if methods, ok := ifaceMethods[name]; ok {
				if n.Meta == nil {
					n.Meta = make(map[string]any)
				}
				n.Meta["methods"] = methods
			}
		}
	}

	// Methods (with class membership).
	matches, _ = parser.RunQuery(javaQClassMethod, e.lang, root, src)
	for _, m := range matches {
		className := m.Captures["class.name"].Text
		name := m.Captures["method.name"].Text
		def := m.Captures["method.def"]
		id := filePath + "::" + className + "." + name
		if seen[id] {
			// Methods can share names (overloads), disambiguate by line.
			id = filePath + "::" + className + "." + name + "_L" + fmt.Sprint(def.StartLine+1)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		// Mark line so fallback query skips this method.
		seen[filePath+"::_method_L"+fmt.Sprint(def.StartLine+1)] = true
		node := &graph.Node{
			ID: id, Kind: graph.KindMethod, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "java",
			Meta:     map[string]any{"receiver": className},
		}
		// Extract return type from the method_declaration node.
		if def.Node != nil {
			if rt := extractJavaMethodReturnType(def.Node, src, e.lang); rt != "" {
				node.Meta["return_type"] = rt
			}
		}
		result.Nodes = append(result.Nodes, node)
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
		// MemberOf edge to containing class.
		classID := filePath + "::" + className
		result.Edges = append(result.Edges, &graph.Edge{
			From: id, To: classID, Kind: graph.EdgeMemberOf, FilePath: filePath, Line: def.StartLine + 1,
		})
		// Spring @Bean factory methods: when a method in a
		// @Configuration class is decorated with @Bean, Spring calls
		// it at context-init to produce a bean of the method's return
		// type. Emit an EdgeProvides from the config class to the
		// method so the indexer's DI post-pass links consumers typed
		// as the return type back to this factory. Without this,
		// `callers:systemClock` on an @Bean method returns empty.
		if def.Node != nil && javaMethodHasAnnotation(def.Node, src, "Bean", e.lang) {
			rt, _ := node.Meta["return_type"].(string)
			if rt != "" {
				result.Edges = append(result.Edges, &graph.Edge{
					From:     classID,
					To:       id,
					Kind:     graph.EdgeProvides,
					FilePath: filePath,
					Line:     def.StartLine + 1,
					Meta: map[string]any{
						"provides_for": rt,
						"binding":      "bean",
					},
				})
			}
		}
	}

	// Fallback: methods not inside a class declaration.
	// Track lines already covered by class-scoped methods to avoid duplicates.
	matches, _ = parser.RunQuery(javaQMethod, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["method.name"].Text
		def := m.Captures["method.def"]
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
			ID: id, Kind: graph.KindMethod, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "java",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Constructors (with class membership).
	matches, _ = parser.RunQuery(javaQClassConstructor, e.lang, root, src)
	for _, m := range matches {
		className := m.Captures["class.name"].Text
		def := m.Captures["ctor.def"]
		id := filePath + "::" + className + ".<init>"
		if seen[id] {
			// Multiple constructors (overloads), disambiguate by line.
			id = filePath + "::" + className + ".<init>_L" + fmt.Sprint(def.StartLine+1)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		// Mark line so fallback query skips this constructor.
		seen[filePath+"::_ctor_L"+fmt.Sprint(def.StartLine+1)] = true
		// Stash param-type text so the indexer's Spring-bean
		// post-pass can match consumers to factory methods by type
		// name. Simpler and more precise than scanning method bodies.
		meta := map[string]any{"receiver": className}
		if params := javaParamsSource(def.Node, src, e.lang); params != "" {
			meta["params_src"] = params
		}
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindMethod, Name: className + ".<init>",
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "java",
			Meta:     meta,
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
		// MemberOf edge to containing class.
		classID := filePath + "::" + className
		result.Edges = append(result.Edges, &graph.Edge{
			From: id, To: classID, Kind: graph.EdgeMemberOf, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Fallback: constructors not matched by class-scoped query.
	matches, _ = parser.RunQuery(javaQConstructor, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["ctor.name"].Text
		def := m.Captures["ctor.def"]
		lineKey := filePath + "::_ctor_L" + fmt.Sprint(def.StartLine+1)
		if seen[lineKey] {
			continue
		}
		id := filePath + "::" + name + ".<init>"
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindMethod, Name: name + ".<init>",
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "java",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Fields (with class membership).
	matches, _ = parser.RunQuery(javaQClassField, e.lang, root, src)
	for _, m := range matches {
		className := m.Captures["class.name"].Text
		name := m.Captures["field.name"].Text
		def := m.Captures["field.def"]
		id := filePath + "::" + className + "." + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindVariable, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "java",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
		classID := filePath + "::" + className
		result.Edges = append(result.Edges, &graph.Edge{
			From: id, To: classID, Kind: graph.EdgeMemberOf, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Imports.
	matches, _ = parser.RunQuery(javaQImport, e.lang, root, src)
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

// extractCalls extracts plain and member method calls with type-env-aware receiver resolution.
func (e *JavaExtractor) extractCalls(root *sitter.Node, src []byte, filePath string, result *parser.ExtractionResult, tenv typeEnv) {
	funcRanges := buildFuncRanges(result)

	// Plain method calls (no receiver): methodName(...)
	matches, _ := parser.RunQuery(javaQCall, e.lang, root, src)
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

	// Member method calls: receiver.methodName(...)
	matches, _ = parser.RunQuery(javaQCallMember, e.lang, root, src)
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

// buildTypeEnv scans Java variable declarations to build a variable-to-type map.
// Tier 0: explicit type declarations (Type varName = ...)
// Tier 1: new expressions (var inferred from object_creation_expression)
func (e *JavaExtractor) buildTypeEnv(root *sitter.Node, src []byte) typeEnv {
	tenv := make(typeEnv)

	// Tier 0: local variable declarations — User user = ...
	matches, _ := parser.RunQuery(javaQLocalVar, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["lvar.name"].Text
		typeName := normalizeJavaTypeName(m.Captures["lvar.type"].Text)
		if typeName != "" {
			tenv[name] = typeName
		}
	}

	// Tier 0: field declarations — private User user = ...
	fieldMatches, _ := parser.RunQuery(javaQFieldVar, e.lang, root, src)
	for _, m := range fieldMatches {
		name := m.Captures["fvar.name"].Text
		if _, exists := tenv[name]; exists {
			continue
		}
		typeName := normalizeJavaTypeName(m.Captures["fvar.type"].Text)
		if typeName != "" {
			tenv[name] = typeName
		}
	}

	// Tier 1: for local vars where explicit type didn't yield a name
	// (e.g., Java 10+ var keyword), try to infer from new expression.
	for _, m := range matches {
		name := m.Captures["lvar.name"].Text
		if _, exists := tenv[name]; exists {
			continue // already have explicit type
		}
		defNode := m.Captures["lvar.def"].Node
		if defNode == nil {
			continue
		}
		walkNodes(defNode, func(n *sitter.Node) {
			if parser.NodeType(n, e.lang) == "object_creation_expression" {
				typeName := inferTypeFromJavaNewExpr(n, src, e.lang)
				if typeName != "" {
					tenv[name] = typeName
				}
			}
		})
	}

	return tenv
}

// normalizeJavaTypeName strips generics and array markers from a Java type name.
// "User" -> "User", "List<User>" -> "List", "User[]" -> "User"
func normalizeJavaTypeName(t string) string {
	t = strings.TrimSpace(t)
	// Remove array suffix.
	t = strings.TrimSuffix(t, "[]")
	// Remove generics.
	if idx := strings.Index(t, "<"); idx > 0 {
		t = t[:idx]
	}
	// Skip Java primitives and common non-class types.
	switch t {
	case "int", "long", "short", "byte", "float", "double", "boolean", "char", "void", "var", "String":
		return ""
	}
	if t == "" || (t[0] >= 'a' && t[0] <= 'z') {
		return "" // skip lowercase type names (primitives)
	}
	return t
}

// extractJavaMethodReturnType walks a method_declaration node to find the return
// type child (typically a type_identifier) and returns the normalized type name.
// javaParamsSource returns the raw source text of a constructor or
// method's formal_parameters child, including the parentheses. Used
// by the DI post-pass to string-match parameter types without a full
// re-parse of the method signature.
func javaParamsSource(methodNode *sitter.Node, src []byte, lang *sitter.Language) string {
	if methodNode == nil {
		return ""
	}
	for i := 0; i < int(methodNode.NamedChildCount()); i++ {
		c := methodNode.NamedChild(i)
		if c != nil && parser.NodeType(c, lang) == "formal_parameters" {
			return c.Text(src)
		}
	}
	return ""
}

// javaMethodHasAnnotation reports whether a method_declaration node
// carries a top-level annotation of the given name (e.g. "Bean",
// "Autowired"). The tree-sitter-java grammar places annotations inside
// a `modifiers` wrapper as either `marker_annotation` (no args) or
// `annotation` (with args). Name is the bare identifier after @.
func javaMethodHasAnnotation(methodNode *sitter.Node, src []byte, name string, lang *sitter.Language) bool {
	for i := 0; i < int(methodNode.NamedChildCount()); i++ {
		c := methodNode.NamedChild(i)
		if c == nil || parser.NodeType(c, lang) != "modifiers" {
			continue
		}
		for j := 0; j < int(c.NamedChildCount()); j++ {
			m := c.NamedChild(j)
			if m == nil {
				continue
			}
			mt := parser.NodeType(m, lang)
			if mt != "marker_annotation" && mt != "annotation" {
				continue
			}
			nameNode := m.ChildByFieldName("name", lang)
			if nameNode != nil && nameNode.Text(src) == name {
				return true
			}
		}
	}
	return false
}

func extractJavaMethodReturnType(methodNode *sitter.Node, src []byte, lang *sitter.Language) string {
	for i := 0; i < int(methodNode.NamedChildCount()); i++ {
		child := methodNode.NamedChild(i)
		switch parser.NodeType(child, lang) {
		case "type_identifier":
			return normalizeJavaTypeName(child.Text(src))
		case "generic_type":
			// e.g., List<User> — take the first named child (the base type).
			if child.NamedChildCount() > 0 {
				return normalizeJavaTypeName(child.NamedChild(0).Text(src))
			}
		case "array_type":
			return normalizeJavaTypeName(child.Text(src))
		}
	}
	return ""
}

// inferTypeFromJavaNewExpr extracts the class name from an object_creation_expression node.
// new User(...) -> "User", new ArrayList<String>() -> "ArrayList"
func inferTypeFromJavaNewExpr(node *sitter.Node, src []byte, lang *sitter.Language) string {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if parser.NodeType(child, lang) == "type_identifier" {
			name := child.Text(src)
			if len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z' {
				return name
			}
		}
	}
	return ""
}
