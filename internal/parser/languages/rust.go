package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

const (
	rsQFunction = `(function_item
		name: (identifier) @func.name) @func.def`

	rsQStruct = `(struct_item
		name: (type_identifier) @type.name) @type.def`

	rsQEnum = `(enum_item
		name: (type_identifier) @type.name) @type.def`

	// Rust enum variants — each arm of a sum type. Variants are
	// first-class constructors and routine navigation targets
	// (`MyEnum::Foo` resolves to the variant), so they deserve nodes
	// with member_of edges back to the enum.
	rsQEnumVariant = `(enum_item
		name: (type_identifier) @enum.name
		body: (enum_variant_list
			(enum_variant
				name: (identifier) @variant.name) @variant.def))`

	// Struct fields — `pub field_name: Type` inside a struct_item.
	// Missing these is the Rust equivalent of missing class
	// properties in TypeScript: routine search targets.
	rsQStructField = `(struct_item
		name: (type_identifier) @struct.name
		body: (field_declaration_list
			(field_declaration
				name: (field_identifier) @field.name) @field.def))`

	rsQTrait = `(trait_item
		name: (type_identifier) @trait.name) @trait.def`

	rsQImplMethod = `(impl_item
		type: (type_identifier) @impl.type
		body: (declaration_list
			(function_item
				name: (identifier) @impl.method.name) @impl.method.def))`

	rsQTraitMethod = `(trait_item
		name: (type_identifier) @trait.name
		body: (declaration_list
			(function_signature_item
				name: (identifier) @trait.method.name)))`

	rsQUse = `(use_declaration
		argument: (_) @use.path) @use.def`

	rsQCall = `(call_expression
		function: (identifier) @call.name) @call.expr`

	rsQCallPath = `(call_expression
		function: (scoped_identifier
			name: (identifier) @call.name)) @call.expr`

	rsQMethodCall = `(call_expression
		function: (field_expression
			value: (_) @call.receiver
			field: (field_identifier) @call.method)) @call.expr`

	rsQConst = `(const_item
		name: (identifier) @const.name) @const.def`

	rsQStatic = `(static_item
		name: (identifier) @static.name) @static.def`

	// Tier 0: let x: Type = ...
	rsQLetTyped = `(let_declaration
		pattern: (identifier) @lvar.name
		type: (_) @lvar.type) @lvar.def`

	// For Tier 1 we walk let_declaration nodes for struct expressions and ::new() calls.
	rsQLet = `(let_declaration
		pattern: (identifier) @let.name
		value: (_) @let.value) @let.def`
)

// RustExtractor extracts Rust source files.
type RustExtractor struct {
	lang *sitter.Language
}

func NewRustExtractor() *RustExtractor {
	return &RustExtractor{lang: grammars.RustLanguage()}
}

func (e *RustExtractor) Language() string     { return "rust" }
func (e *RustExtractor) Extensions() []string { return []string{".rs"} }

func (e *RustExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "rust",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)

	// Impl methods (must run before functions to filter them out).
	implMethodLines := make(map[int]bool)
	matches, _ := parser.RunQuery(rsQImplMethod, e.lang, root, src)
	for _, m := range matches {
		typeName := m.Captures["impl.type"].Text
		methodName := m.Captures["impl.method.name"].Text
		def := m.Captures["impl.method.def"]
		id := filePath + "::" + typeName + "." + methodName
		if seen[id] {
			continue
		}
		seen[id] = true
		implMethodLines[def.StartLine] = true
		meta := map[string]any{
			"receiver":  typeName,
			"signature": "fn " + methodName + "(...)",
		}
		if rt := extractRustReturnType(def.Node, src, e.lang); rt != "" {
			meta["return_type"] = rt
		}
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindMethod, Name: methodName,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "rust", Meta: meta,
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
		typeID := filePath + "::" + typeName
		result.Edges = append(result.Edges, &graph.Edge{
			From: id, To: typeID, Kind: graph.EdgeMemberOf, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Functions (skip those already extracted as impl methods).
	matches, _ = parser.RunQuery(rsQFunction, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["func.name"].Text
		def := m.Captures["func.def"]
		if implMethodLines[def.StartLine] {
			continue
		}
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindFunction, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "rust", Meta: map[string]any{"signature": "fn " + name + "(...)"},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Structs.
	matches, _ = parser.RunQuery(rsQStruct, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["type.name"].Text
		def := m.Captures["type.def"]
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindType, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "rust",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Enums.
	matches, _ = parser.RunQuery(rsQEnum, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["type.name"].Text
		def := m.Captures["type.def"]
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindType, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "rust",
			Meta:     map[string]any{"kind": "enum"},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Enum variants — each arm of a sum type becomes a member node.
	variantMatches, _ := parser.RunQuery(rsQEnumVariant, e.lang, root, src)
	for _, m := range variantMatches {
		enumName := m.Captures["enum.name"].Text
		variantName := m.Captures["variant.name"].Text
		variantDef := m.Captures["variant.def"]
		enumID := filePath + "::" + enumName
		variantID := enumID + "." + variantName
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: variantID, Kind: graph.KindVariable, Name: variantName,
			FilePath:  filePath,
			StartLine: variantDef.StartLine + 1,
			EndLine:   variantDef.EndLine + 1,
			Language:  "rust",
			Meta:      map[string]any{"receiver": enumName, "kind": "enum_variant"},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: variantID, To: enumID, Kind: graph.EdgeMemberOf,
			FilePath: filePath, Line: variantDef.StartLine + 1,
		})
	}

	// Struct fields — pub / private named fields inside a struct_item.
	fieldMatches, _ := parser.RunQuery(rsQStructField, e.lang, root, src)
	for _, m := range fieldMatches {
		structName := m.Captures["struct.name"].Text
		fieldName := m.Captures["field.name"].Text
		fieldDef := m.Captures["field.def"]
		structID := filePath + "::" + structName
		fieldID := structID + "." + fieldName
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: fieldID, Kind: graph.KindVariable, Name: fieldName,
			FilePath:  filePath,
			StartLine: fieldDef.StartLine + 1,
			EndLine:   fieldDef.EndLine + 1,
			Language:  "rust",
			Meta:      map[string]any{"receiver": structName, "kind": "struct_field"},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fieldID, To: structID, Kind: graph.EdgeMemberOf,
			FilePath: filePath, Line: fieldDef.StartLine + 1,
		})
	}

	// Trait method specs (collect before creating trait nodes).
	traitMethods := make(map[string][]string)
	matches, _ = parser.RunQuery(rsQTraitMethod, e.lang, root, src)
	for _, m := range matches {
		tName := m.Captures["trait.name"].Text
		mName := m.Captures["trait.method.name"].Text
		traitMethods[tName] = append(traitMethods[tName], mName)
	}

	// Traits.
	matches, _ = parser.RunQuery(rsQTrait, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["trait.name"].Text
		def := m.Captures["trait.def"]
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		meta := map[string]any{}
		if methods, ok := traitMethods[name]; ok {
			meta["methods"] = methods
		}
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindInterface, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "rust", Meta: meta,
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Use declarations (imports).
	matches, _ = parser.RunQuery(rsQUse, e.lang, root, src)
	for _, m := range matches {
		path := m.Captures["use.path"]
		usePath := strings.ReplaceAll(path.Text, "::", "/")
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + usePath,
			Kind: graph.EdgeImports, FilePath: filePath, Line: path.StartLine + 1,
		})
	}

	// Build type environment for receiver type inference.
	tenv := e.buildTypeEnv(root, src)

	// Call sites (with type env).
	e.extractCalls(root, src, filePath, result, tenv)

	// Constants and statics.
	for _, q := range []string{rsQConst, rsQStatic} {
		matches, _ = parser.RunQuery(q, e.lang, root, src)
		for _, m := range matches {
			var name string
			var def *parser.CapturedNode
			if c, ok := m.Captures["const.name"]; ok {
				name = c.Text
				def = m.Captures["const.def"]
			} else if c, ok := m.Captures["static.name"]; ok {
				name = c.Text
				def = m.Captures["static.def"]
			}
			if name == "" {
				continue
			}
			id := filePath + "::" + name
			if seen[id] {
				continue
			}
			seen[id] = true
			result.Nodes = append(result.Nodes, &graph.Node{
				ID: id, Kind: graph.KindVariable, Name: name,
				FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
				Language: "rust",
			})
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
			})
		}
	}

	return result, nil
}

func (e *RustExtractor) extractCalls(root *sitter.Node, src []byte, filePath string, result *parser.ExtractionResult, tenv typeEnv) {
	funcRanges := buildFuncRanges(result)

	// Plain and path calls: foo(), module::foo()
	for _, q := range []string{rsQCall, rsQCallPath} {
		matches, _ := parser.RunQuery(q, e.lang, root, src)
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
	}

	// Method calls: obj.method()
	matches, _ := parser.RunQuery(rsQMethodCall, e.lang, root, src)
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

// buildTypeEnv scans Rust let declarations for type annotations (Tier 0)
// and struct expressions / ::new() calls (Tier 1) to build a variable-to-type map.
func (e *RustExtractor) buildTypeEnv(root *sitter.Node, src []byte) typeEnv {
	tenv := make(typeEnv)

	// Tier 0: explicit type annotations — let x: Type = ...
	matches, _ := parser.RunQuery(rsQLetTyped, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["lvar.name"].Text
		typeName := normalizeRustTypeName(m.Captures["lvar.type"].Text)
		if typeName != "" {
			tenv[name] = typeName
		}
	}

	// Tier 1: infer from RHS — struct expressions and ::new() calls.
	matches, _ = parser.RunQuery(rsQLet, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["let.name"].Text
		if _, exists := tenv[name]; exists {
			continue
		}
		valueNode := m.Captures["let.value"].Node
		if valueNode == nil {
			continue
		}
		if inferred := inferTypeFromRustExpr(valueNode, src, e.lang); inferred != "" {
			tenv[name] = inferred
		}
	}

	return tenv
}

// extractRustReturnType walks a function_item node to find the return type after `->`.
func extractRustReturnType(node *sitter.Node, src []byte, lang *sitter.Language) string {
	if node == nil {
		return ""
	}
	// In tree-sitter-rust, function_item has children: fn, name, parameters, ->, type, block.
	// Look for a type child after "->".
	pastArrow := false
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		text := string(src[child.StartByte():child.EndByte()])
		if text == "->" {
			pastArrow = true
			continue
		}
		if pastArrow {
			if parser.NodeType(child, lang) == "block" {
				return ""
			}
			// This should be the return type node.
			rawType := string(src[child.StartByte():child.EndByte()])
			if rt := normalizeRustTypeName(rawType); rt != "" {
				return rt
			}
			return ""
		}
	}
	return ""
}

// normalizeRustTypeName strips references, generics, and module paths from a Rust type.
func normalizeRustTypeName(t string) string {
	t = strings.TrimSpace(t)
	// Remove reference prefixes.
	t = strings.TrimPrefix(t, "&mut ")
	t = strings.TrimPrefix(t, "&")
	// Remove generics.
	if idx := strings.Index(t, "<"); idx > 0 {
		t = t[:idx]
	}
	// Take last segment of module path.
	if idx := strings.LastIndex(t, "::"); idx >= 0 {
		t = t[idx+2:]
	}
	// Skip primitives.
	switch t {
	case "i8", "i16", "i32", "i64", "i128", "isize",
		"u8", "u16", "u32", "u64", "u128", "usize",
		"f32", "f64", "bool", "char", "str", "String",
		"Self", "self":
		return ""
	}
	if t == "" || (t[0] >= 'a' && t[0] <= 'z') {
		return ""
	}
	return t
}

// inferTypeFromRustExpr inspects a tree-sitter expression node to infer
// the type of a let declaration's RHS.
func inferTypeFromRustExpr(node *sitter.Node, src []byte, lang *sitter.Language) string {
	switch parser.NodeType(node, lang) {
	case "struct_expression":
		// Config { port: 8080 } — first named child is the type name.
		if node.NamedChildCount() > 0 {
			typeNode := node.NamedChild(0)
			name := typeNode.Text(src)
			// Strip module path.
			if idx := strings.LastIndex(name, "::"); idx >= 0 {
				name = name[idx+2:]
			}
			if len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z' {
				return name
			}
		}

	case "call_expression":
		// Type::new() — scoped_identifier with path containing ::new
		if node.NamedChildCount() > 0 {
			funcNode := node.NamedChild(0)
			if parser.NodeType(funcNode, lang) == "scoped_identifier" {
				funcText := funcNode.Text(src)
				// e.g. "Config::new" or "module::Config::new"
				if strings.HasSuffix(funcText, "::new") {
					typePart := strings.TrimSuffix(funcText, "::new")
					// Take last segment.
					if idx := strings.LastIndex(typePart, "::"); idx >= 0 {
						typePart = typePart[idx+2:]
					}
					if len(typePart) > 0 && typePart[0] >= 'A' && typePart[0] <= 'Z' {
						return typePart
					}
				}
			}
		}
	}

	return ""
}
