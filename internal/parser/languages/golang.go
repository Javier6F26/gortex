package languages

import (
	"fmt"
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// Tree-sitter query patterns for Go source files.
const (
	qPackage = `(package_clause (package_identifier) @pkg.name)`

	qFunction = `(function_declaration
		name: (identifier) @func.name
		parameters: (parameter_list) @func.params
		result: (_)? @func.result) @func.def`

	qMethod = `(method_declaration
		receiver: (parameter_list) @method.receiver
		name: (field_identifier) @method.name
		parameters: (parameter_list) @method.params
		result: (_)? @method.result) @method.def`

	qStruct = `(type_declaration
		(type_spec
			name: (type_identifier) @type.name
			type: (struct_type) @type.body)) @type.def`

	qInterface = `(type_declaration
		(type_spec
			name: (type_identifier) @iface.name
			type: (interface_type) @iface.body)) @iface.def`

	qTypeOther = `(type_declaration
		(type_spec
			name: (type_identifier) @typedef.name
			type: (_) @typedef.type)) @typedef.def`

	qTypeAlias = `(type_declaration
		(type_alias
			name: (type_identifier) @alias.name
			type: (_) @alias.type)) @alias.def`

	qImportSingle = `(import_declaration
		(import_spec
			name: (package_identifier)? @import.alias
			path: (interpreted_string_literal) @import.path))`

	qImportBlock = `(import_declaration
		(import_spec_list
			(import_spec
				name: (package_identifier)? @import.alias
				path: (interpreted_string_literal) @import.path)))`

	qCallPlain = `(call_expression
		function: (identifier) @call.name) @call.expr`

	qCallSelector = `(call_expression
		function: (selector_expression
			operand: (_) @call.receiver
			field: (field_identifier) @call.method)) @call.expr`

	qVar = `(var_declaration
		(var_spec
			name: (identifier) @var.name
			type: (_)? @var.type)) @var.def`

	qConst = `(const_declaration
		(const_spec
			name: (identifier) @const.name)) @const.def`

	qShortVar = `(short_var_declaration
		left: (expression_list (identifier) @svar.name)
		right: (expression_list (_) @svar.value)) @svar.def`

	// --- Dead-code-accuracy queries ---

	// Composite literals: MyType{...}, &MyType{...}, pkg.Type{...}
	qCompositeLiteral = `(composite_literal
		type: (type_identifier) @comp.type) @comp.expr`

	qCompositeLiteralQualified = `(composite_literal
		type: (qualified_type
			package: (package_identifier) @comp.pkg
			name: (type_identifier) @comp.type)) @comp.expr`

	// Type assertions: x.(MyType), x.(pkg.Type)
	qTypeAssert = `(type_assertion
		type: (type_identifier) @assert.type) @assert.expr`

	qTypeAssertQualified = `(type_assertion
		type: (qualified_type
			package: (package_identifier) @assert.pkg
			name: (type_identifier) @assert.type)) @assert.expr`

	// Function/method values passed as arguments (not invoked directly).
	// Selector expression inside argument_list but NOT inside call_expression function position.
	qSelectorArg = `(argument_list
		(selector_expression
			operand: (_) @selarg.receiver
			field: (field_identifier) @selarg.field)) @selarg.list`

	// Bare identifier passed as argument (function value).
	qIdentArg = `(argument_list
		(identifier) @identarg.name) @identarg.list`

	// --- Type references in declarations ---

	// Type identifier in struct field declarations: field apiFormat
	qFieldType = `(field_declaration
		type: (type_identifier) @ftype.name) @ftype.decl`

	// Type identifier in const/var declarations: const x apiFormat = ...
	qConstType = `(const_spec
		type: (type_identifier) @ctype.name) @ctype.decl`

	qVarType = `(var_spec
		type: (type_identifier) @vtype.name) @vtype.decl`

	// Type identifier in parameter lists: func foo(x apiFormat)
	qParamType = `(parameter_declaration
		type: (type_identifier) @ptype.name) @ptype.decl`

	// --- Struct literal field value references ---

	// Bare identifier as a struct field value: &cobra.Command{RunE: runClean}
	// AST: keyed_element → literal_element(identifier key) → literal_element(identifier value)
	qFieldValueIdent = `(keyed_element
		(literal_element (identifier) @fieldval.key)
		(literal_element (identifier) @fieldval.value)) @fieldval.elem`

	// Selector expression as a struct field value: {Handler: h.handleHealth}
	qFieldValueSelector = `(keyed_element
		(literal_element (identifier) @fieldsel.key)
		(literal_element
			(selector_expression
				operand: (_) @fieldsel.receiver
				field: (field_identifier) @fieldsel.method))) @fieldsel.elem`
)

// GoExtractor extracts Go source files into graph nodes and edges.
type GoExtractor struct {
	lang *sitter.Language
}

func NewGoExtractor() *GoExtractor {
	return &GoExtractor{lang: grammars.GoLanguage()}
}

func (e *GoExtractor) Language() string     { return "go" }
func (e *GoExtractor) Extensions() []string { return []string{".go"} }

func (e *GoExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	tree, err := parser.ParseFile(src, e.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	root := tree.RootNode()
	result := &parser.ExtractionResult{}

	// File node.
	fileNode := &graph.Node{
		ID:        filePath,
		Kind:      graph.KindFile,
		Name:      filePath,
		FilePath:  filePath,
		StartLine: 1,
		EndLine:   int(root.EndPoint().Row) + 1,
		Language:  "go",
	}
	result.Nodes = append(result.Nodes, fileNode)

	// Package declaration.
	pkgName := e.extractPackage(root, src, filePath, result)

	// Functions.
	e.extractFunctions(root, src, filePath, fileNode.ID, result)

	// Methods.
	e.extractMethods(root, src, filePath, fileNode.ID, result)

	// Types: structs, interfaces, type aliases.
	e.extractTypes(root, src, filePath, fileNode.ID, result)

	// Imports. Returned map is alias→importPath so downstream call-site
	// extraction can attribute selector calls like `json.NewEncoder` to
	// the owning package instead of the generic `unresolved::*.Method`.
	imports := e.extractImports(root, src, filePath, fileNode.ID, pkgName, result)

	// Build type environment for receiver type inference.
	tenv := e.buildTypeEnv(root, src)

	// Call sites (with type env for receiver resolution, plus imports
	// so imported-package calls get a stable extern:: target).
	e.extractCalls(root, src, filePath, result, tenv, imports)

	// Variables and constants.
	e.extractVarsConsts(root, src, filePath, fileNode.ID, result)

	// Type references: composite literals (struct instantiation), type assertions.
	e.extractTypeRefs(root, src, filePath, result)

	// Value references: function/method identifiers passed as callbacks.
	e.extractValueRefs(root, src, filePath, result, tenv)

	return result, nil
}

func (e *GoExtractor) extractPackage(root *sitter.Node, src []byte, filePath string, result *parser.ExtractionResult) string {
	matches, err := parser.RunQuery(qPackage, e.lang, root, src)
	if err != nil || len(matches) == 0 {
		return ""
	}
	name := matches[0].Captures["pkg.name"].Text
	return name
}

func (e *GoExtractor) extractFunctions(root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult) {
	matches, err := parser.RunQuery(qFunction, e.lang, root, src)
	if err != nil {
		return
	}
	for _, m := range matches {
		name := m.Captures["func.name"].Text
		def := m.Captures["func.def"]

		id := filePath + "::" + name
		node := &graph.Node{
			ID:        id,
			Kind:      graph.KindFunction,
			Name:      name,
			FilePath:  filePath,
			StartLine: def.StartLine + 1,
			EndLine:   def.EndLine + 1,
			Language:  "go",
			Meta:      make(map[string]any),
		}
		node.Meta["signature"] = buildFuncSignature(name, m.Captures["func.params"], m.Captures["func.result"])
		if resultCap, ok := m.Captures["func.result"]; ok && resultCap.Text != "" {
			if rt := normalizeGoTypeName(resultCap.Text); rt != "" {
				node.Meta["return_type"] = rt
			}
		}
		scanGoPragmas(src, def.StartLine, node)
		result.Nodes = append(result.Nodes, node)
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}
}

func (e *GoExtractor) extractMethods(root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult) {
	matches, err := parser.RunQuery(qMethod, e.lang, root, src)
	if err != nil {
		return
	}
	for _, m := range matches {
		name := m.Captures["method.name"].Text
		def := m.Captures["method.def"]
		receiverText := m.Captures["method.receiver"].Text
		receiverType := extractReceiverType(receiverText)

		id := filePath + "::" + receiverType + "." + name
		node := &graph.Node{
			ID:        id,
			Kind:      graph.KindMethod,
			Name:      name,
			FilePath:  filePath,
			StartLine: def.StartLine + 1,
			EndLine:   def.EndLine + 1,
			Language:  "go",
			Meta: map[string]any{
				"receiver": receiverType,
			},
		}
		node.Meta["signature"] = buildMethodSignature(receiverText, name, m.Captures["method.params"], m.Captures["method.result"])
		if resultCap, ok := m.Captures["method.result"]; ok && resultCap.Text != "" {
			if rt := normalizeGoTypeName(resultCap.Text); rt != "" {
				node.Meta["return_type"] = rt
			}
		}
		scanGoPragmas(src, def.StartLine, node)
		result.Nodes = append(result.Nodes, node)
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})

		// MemberOf edge to receiver type.
		typeID := filePath + "::" + receiverType
		result.Edges = append(result.Edges, &graph.Edge{
			From: id, To: typeID, Kind: graph.EdgeMemberOf, FilePath: filePath, Line: def.StartLine + 1,
		})
	}
}

func (e *GoExtractor) extractTypes(root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult) {
	// Track which names we've already added to avoid duplicates between
	// struct/interface queries and the general type alias query.
	seen := make(map[string]bool)

	// Structs.
	matches, _ := parser.RunQuery(qStruct, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["type.name"].Text
		def := m.Captures["type.def"]
		seen[name] = true
		id := filePath + "::" + name
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindType, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "go",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Interfaces.
	matches, _ = parser.RunQuery(qInterface, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["iface.name"].Text
		def := m.Captures["iface.def"]
		seen[name] = true
		id := filePath + "::" + name

		// Extract method specs from the interface body by walking child nodes.
		var methods []string
		if body := m.Captures["iface.body"]; body != nil && body.Node != nil {
			methods = extractInterfaceMethods(body.Node, src, e.lang)
		}

		node := &graph.Node{
			ID: id, Kind: graph.KindInterface, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "go",
			Meta:     map[string]any{"methods": methods},
		}
		result.Nodes = append(result.Nodes, node)
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Other type declarations (named types that aren't struct/interface).
	matches, _ = parser.RunQuery(qTypeOther, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["typedef.name"].Text
		if seen[name] {
			continue
		}
		seen[name] = true
		def := m.Captures["typedef.def"]
		id := filePath + "::" + name
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindType, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "go",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// True type aliases (type X = Y).
	matches, _ = parser.RunQuery(qTypeAlias, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["alias.name"].Text
		if seen[name] {
			continue
		}
		def := m.Captures["alias.def"]
		id := filePath + "::" + name
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindType, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "go",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}
}

// extractImports emits one EdgeImports per import spec and returns a
// per-file alias→importPath map. The alias is the explicit one when
// present, else the last path segment (Go's default). Blank and dot
// imports are skipped in the map — they don't introduce a callable
// identifier (only side-effects / merged names).
func (e *GoExtractor) extractImports(root *sitter.Node, src []byte, filePath, fileID, _ string, result *parser.ExtractionResult) map[string]string {
	imports := map[string]string{}
	for _, q := range []string{qImportSingle, qImportBlock} {
		matches, err := parser.RunQuery(q, e.lang, root, src)
		if err != nil {
			continue
		}
		for _, m := range matches {
			pathCap := m.Captures["import.path"]
			importPath := strings.Trim(pathCap.Text, `"`)
			result.Edges = append(result.Edges, &graph.Edge{
				From:     fileID,
				To:       "unresolved::import::" + importPath,
				Kind:     graph.EdgeImports,
				FilePath: filePath,
				Line:     pathCap.StartLine + 1,
			})
			alias := ""
			if a, ok := m.Captures["import.alias"]; ok {
				alias = strings.TrimSpace(a.Text)
			}
			switch alias {
			case "_", ".":
				continue
			case "":
				alias = importPath
				if i := strings.LastIndex(importPath, "/"); i >= 0 {
					alias = importPath[i+1:]
				}
			}
			imports[alias] = importPath
		}
	}
	return imports
}

func (e *GoExtractor) extractCalls(root *sitter.Node, src []byte, filePath string, result *parser.ExtractionResult, tenv typeEnv, imports map[string]string) {
	funcRanges := buildFuncRanges(result)

	// Plain function calls: foo()
	matches, _ := parser.RunQuery(qCallPlain, e.lang, root, src)
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

	// Selector calls: x.Method()
	matches, _ = parser.RunQuery(qCallSelector, e.lang, root, src)
	for _, m := range matches {
		methodName := m.Captures["call.method"].Text
		receiverText := m.Captures["call.receiver"].Text
		expr := m.Captures["call.expr"]
		callerID := findEnclosingFunc(funcRanges, expr.StartLine+1)
		if callerID == "" {
			continue
		}

		// Package-qualified call (json.NewEncoder, fmt.Println, …): use
		// the import path as the extern target so the resolver can
		// classify it as stdlib / dep / cross-repo and the UI can render
		// a meaningful "crosses web → encoding/json" label. Takes
		// precedence over receiver-type inference — a bare identifier
		// that matches an import alias is always a package reference.
		if importPath, ok := imports[receiverText]; ok {
			result.Edges = append(result.Edges, &graph.Edge{
				From:     callerID,
				To:       "unresolved::extern::" + importPath + "::" + methodName,
				Kind:     graph.EdgeCalls,
				FilePath: filePath,
				Line:     expr.StartLine + 1,
			})
			continue
		}

		edge := &graph.Edge{
			From:     callerID,
			To:       "unresolved::*." + methodName,
			Kind:     graph.EdgeCalls,
			FilePath: filePath,
			Line:     expr.StartLine + 1,
		}

		// Attach receiver type hint from type env (Tier 0+1) or chain resolution (Tier 2).
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

func (e *GoExtractor) extractVarsConsts(root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult) {
	for _, q := range []string{qVar, qConst} {
		matches, err := parser.RunQuery(q, e.lang, root, src)
		if err != nil {
			continue
		}
		for _, m := range matches {
			var name string
			var def *parser.CapturedNode
			if c, ok := m.Captures["var.name"]; ok {
				name = c.Text
				def = m.Captures["var.def"]
			} else if c, ok := m.Captures["const.name"]; ok {
				name = c.Text
				def = m.Captures["const.def"]
			}
			if name == "" || name == "_" {
				continue
			}
			id := filePath + "::" + name
			result.Nodes = append(result.Nodes, &graph.Node{
				ID: id, Kind: graph.KindVariable, Name: name,
				FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
				Language: "go",
			})
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
			})
		}
	}
}

// extractTypeRefs emits EdgeReferences edges for composite literals (struct
// instantiation) and type assertions/conversions, and EdgeReferences edges for
// function/method values passed as arguments (not invoked).  These references
// are invisible to the basic call-graph but are critical for dead-code accuracy.
func (e *GoExtractor) extractTypeRefs(root *sitter.Node, src []byte, filePath string, result *parser.ExtractionResult) {
	funcRanges := buildFuncRanges(result)

	// 1. Composite literals: MyType{...}
	for _, q := range []string{qCompositeLiteral, qCompositeLiteralQualified} {
		matches, _ := parser.RunQuery(q, e.lang, root, src)
		for _, m := range matches {
			typeName := m.Captures["comp.type"].Text
			expr := m.Captures["comp.expr"]
			callerID := findEnclosingFunc(funcRanges, expr.StartLine+1)
			if callerID == "" {
				callerID = filePath // package-level expression → use file node
			}
			result.Edges = append(result.Edges, &graph.Edge{
				From:     callerID,
				To:       "unresolved::" + typeName,
				Kind:     graph.EdgeInstantiates,
				FilePath: filePath,
				Line:     expr.StartLine + 1,
			})
		}
	}

	// 2. Type assertions: x.(MyType)
	for _, q := range []string{qTypeAssert, qTypeAssertQualified} {
		matches, _ := parser.RunQuery(q, e.lang, root, src)
		for _, m := range matches {
			typeName := m.Captures["assert.type"].Text
			expr := m.Captures["assert.expr"]
			callerID := findEnclosingFunc(funcRanges, expr.StartLine+1)
			if callerID == "" {
				callerID = filePath
			}
			result.Edges = append(result.Edges, &graph.Edge{
				From:     callerID,
				To:       "unresolved::" + typeName,
				Kind:     graph.EdgeReferences,
				FilePath: filePath,
				Line:     expr.StartLine + 1,
			})
		}
	}

	// 3. Type references in declarations: struct fields, const types, var types, parameters.
	// These reference the type without calling or instantiating it.
	typeRefQueries := []struct {
		query      string
		captureKey string
	}{
		{qFieldType, "ftype"},
		{qConstType, "ctype"},
		{qVarType, "vtype"},
		{qParamType, "ptype"},
	}
	for _, tq := range typeRefQueries {
		matches, _ := parser.RunQuery(tq.query, e.lang, root, src)
		for _, m := range matches {
			typeName := m.Captures[tq.captureKey+".name"].Text
			decl := m.Captures[tq.captureKey+".decl"]
			callerID := findEnclosingFunc(funcRanges, decl.StartLine+1)
			if callerID == "" {
				callerID = filePath
			}
			result.Edges = append(result.Edges, &graph.Edge{
				From:     callerID,
				To:       "unresolved::" + typeName,
				Kind:     graph.EdgeReferences,
				FilePath: filePath,
				Line:     decl.StartLine + 1,
			})
		}
	}
}

// extractValueRefs emits EdgeReferences edges for function/method identifiers
// passed as values (callbacks, handler registrations, etc.) rather than being
// called directly.  For example:  h.mux.HandleFunc("GET /health", h.handleHealth)
// creates a reference edge from registerRoutes → handleHealth.
func (e *GoExtractor) extractValueRefs(root *sitter.Node, src []byte, filePath string, result *parser.ExtractionResult, tenv typeEnv) {
	funcRanges := buildFuncRanges(result)

	// Selector expressions inside argument lists: h.handleHealth as an arg
	matches, _ := parser.RunQuery(qSelectorArg, e.lang, root, src)
	for _, m := range matches {
		fieldName := m.Captures["selarg.field"].Text
		receiverText := m.Captures["selarg.receiver"].Text
		listNode := m.Captures["selarg.list"]
		callerID := findEnclosingFunc(funcRanges, listNode.StartLine+1)
		if callerID == "" {
			callerID = filePath // package-level expression
		}

		// NOTE: We previously skipped uppercase field names here as a heuristic
		// to avoid package-qualified calls (pkg.Func()). However, the tree-sitter
		// query qSelectorArg only matches selectors inside argument_list, never
		// in call function position, so the skip was over-broad and suppressed
		// legitimate method value references like http.HandlerFunc(s.ServeHTTP).

		edge := &graph.Edge{
			From:     callerID,
			To:       "unresolved::*." + fieldName,
			Kind:     graph.EdgeReferences,
			FilePath: filePath,
			Line:     listNode.StartLine + 1,
		}

		// Attach receiver type hint if available.
		if recvType, ok := tenv[receiverText]; ok {
			edge.Meta = map[string]any{"receiver_type": recvType}
		}

		result.Edges = append(result.Edges, edge)
	}

	// Bare identifiers inside argument lists: funcName as an arg
	matches, _ = parser.RunQuery(qIdentArg, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["identarg.name"].Text
		listNode := m.Captures["identarg.list"]
		callerID := findEnclosingFunc(funcRanges, listNode.StartLine+1)
		if callerID == "" {
			callerID = filePath
		}

		// Skip common non-function identifiers (keywords, builtins, etc.)
		if isGoBuiltinOrKeyword(name) {
			continue
		}

		result.Edges = append(result.Edges, &graph.Edge{
			From:     callerID,
			To:       "unresolved::" + name,
			Kind:     graph.EdgeReferences,
			FilePath: filePath,
			Line:     listNode.StartLine + 1,
		})
	}

	// Bare identifiers as struct field values: &cobra.Command{RunE: runClean}
	matches, _ = parser.RunQuery(qFieldValueIdent, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["fieldval.value"].Text
		elem := m.Captures["fieldval.elem"]
		callerID := findEnclosingFunc(funcRanges, elem.StartLine+1)
		if callerID == "" {
			callerID = filePath
		}
		if isGoBuiltinOrKeyword(name) {
			continue
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From:     callerID,
			To:       "unresolved::" + name,
			Kind:     graph.EdgeReferences,
			FilePath: filePath,
			Line:     elem.StartLine + 1,
		})
	}

	// Selector expressions as struct field values: {Handler: h.handleHealth}
	matches, _ = parser.RunQuery(qFieldValueSelector, e.lang, root, src)
	for _, m := range matches {
		methodName := m.Captures["fieldsel.method"].Text
		receiverText := m.Captures["fieldsel.receiver"].Text
		elem := m.Captures["fieldsel.elem"]
		callerID := findEnclosingFunc(funcRanges, elem.StartLine+1)
		if callerID == "" {
			callerID = filePath
		}

		edge := &graph.Edge{
			From:     callerID,
			To:       "unresolved::*." + methodName,
			Kind:     graph.EdgeReferences,
			FilePath: filePath,
			Line:     elem.StartLine + 1,
		}
		if recvType, ok := tenv[receiverText]; ok {
			edge.Meta = map[string]any{"receiver_type": recvType}
		}
		result.Edges = append(result.Edges, edge)
	}
}

// isGoBuiltinOrKeyword returns true for identifiers that should not be treated
// as function-value references (common Go builtins, type names, and literals).
func isGoBuiltinOrKeyword(name string) bool {
	switch name {
	case "nil", "true", "false", "err", "ok", "ctx",
		"string", "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64",
		"float32", "float64", "complex64", "complex128",
		"bool", "byte", "rune", "error", "any",
		"len", "cap", "make", "new", "append", "copy", "delete",
		"close", "panic", "recover", "print", "println",
		"real", "imag", "complex", "clear", "min", "max",
		"_", "i", "j", "k", "n", "s", "t", "v", "b", "w", "r":
		return true
	}
	return false
}

// scanGoPragmas inspects up to 5 source lines immediately before a function or
// method declaration for Go compiler pragmas (//export, //go:linkname) and sets
// the corresponding Meta keys on the node.  startLine is 0-based (tree-sitter).
func scanGoPragmas(src []byte, startLine int, node *graph.Node) {
	// Walk backward through src to find the start of each preceding line.
	// We scan at most 5 lines before the declaration.
	lineNum := 0 // 0-based line number
	lineStarts := make([]int, 0, startLine+1)
	for i := range src {
		if lineNum > startLine {
			break
		}
		if i == 0 || src[i-1] == '\n' {
			lineStarts = append(lineStarts, i)
			lineNum++
		}
	}

	for scanLine := startLine - 1; scanLine >= 0 && scanLine >= startLine-5; scanLine-- {
		if scanLine >= len(lineStarts) {
			continue
		}
		start := lineStarts[scanLine]
		end := len(src)
		if scanLine+1 < len(lineStarts) {
			end = lineStarts[scanLine+1]
		}
		line := strings.TrimSpace(string(src[start:end]))

		// Stop scanning if we hit a non-comment, non-empty line (the previous
		// declaration's body).
		if line != "" && !strings.HasPrefix(line, "//") {
			break
		}

		if strings.HasPrefix(line, "//export ") {
			node.Meta["cgo_export"] = true
			return
		}
		if strings.HasPrefix(line, "//go:linkname ") {
			node.Meta["go_linkname"] = true
			return
		}
	}
}

// --- Helpers ---

type funcRange struct {
	id        string
	startLine int // 1-based
	endLine   int // 1-based
}

func buildFuncRanges(result *parser.ExtractionResult) []funcRange {
	var ranges []funcRange
	for _, n := range result.Nodes {
		if n.Kind == graph.KindFunction || n.Kind == graph.KindMethod {
			ranges = append(ranges, funcRange{
				id: n.ID, startLine: n.StartLine, endLine: n.EndLine,
			})
		}
	}
	return ranges
}

func findEnclosingFunc(ranges []funcRange, line int) string {
	for _, r := range ranges {
		if line >= r.startLine && line <= r.endLine {
			return r.id
		}
	}
	return ""
}

// extractReceiverType extracts the type name from a Go receiver parameter list.
// "(s *Server)" -> "Server", "(s Server)" -> "Server"
func extractReceiverType(receiver string) string {
	receiver = strings.Trim(receiver, "()")
	parts := strings.Fields(receiver)
	if len(parts) == 0 {
		return ""
	}
	typePart := parts[len(parts)-1]
	typePart = strings.TrimPrefix(typePart, "*")
	return typePart
}

func buildFuncSignature(name string, params, result *parser.CapturedNode) string {
	sig := fmt.Sprintf("func %s%s", name, captureText(params))
	if result != nil && result.Text != "" {
		sig += " " + result.Text
	}
	return sig
}

func buildMethodSignature(receiver, name string, params, result *parser.CapturedNode) string {
	sig := fmt.Sprintf("func (%s) %s%s", receiver, name, captureText(params))
	if result != nil && result.Text != "" {
		sig += " " + result.Text
	}
	return sig
}

// extractInterfaceMethods walks the children of an interface_type node
// and returns the names of all method_spec entries.
func extractInterfaceMethods(ifaceNode *sitter.Node, src []byte, lang *sitter.Language) []string {
	var methods []string
	for i := 0; i < int(ifaceNode.NamedChildCount()); i++ {
		child := ifaceNode.NamedChild(i)
		childType := parser.NodeType(child, lang)
		if childType == "method_elem" || childType == "method_spec" {
			// The first named child of a method_spec is the field_identifier (name).
			for j := 0; j < int(child.NamedChildCount()); j++ {
				nameNode := child.NamedChild(j)
				if parser.NodeType(nameNode, lang) == "field_identifier" {
					methods = append(methods, nameNode.Text(src))
					break
				}
			}
		}
	}
	return methods
}

func captureText(c *parser.CapturedNode) string {
	if c == nil {
		return "()"
	}
	return c.Text
}

// --- Type environment for receiver type inference ---

// typeEnv maps variable name → inferred type name within a file.
type typeEnv map[string]string

// buildTypeEnv scans variable declarations and short variable declarations
// to infer types (Tier 0: explicit annotations, Tier 1: composite literals
// and Go constructor convention).
func (e *GoExtractor) buildTypeEnv(root *sitter.Node, src []byte) typeEnv {
	tenv := make(typeEnv)

	// Tier 0: explicit var declarations — var x Type
	matches, _ := parser.RunQuery(qVar, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["var.name"].Text
		if typeCap, ok := m.Captures["var.type"]; ok && typeCap.Text != "" {
			typeName := normalizeGoTypeName(typeCap.Text)
			if typeName != "" {
				tenv[name] = typeName
			}
		}
	}

	// Tier 0 + Tier 1: short variable declarations — x := expr
	matches, _ = parser.RunQuery(qShortVar, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["svar.name"].Text
		valueCap := m.Captures["svar.value"]
		if valueCap == nil || valueCap.Node == nil {
			continue
		}
		if inferred := inferTypeFromGoExpr(valueCap.Node, src, e.lang); inferred != "" {
			tenv[name] = inferred
		}
	}

	return tenv
}

// normalizeGoTypeName strips pointer prefix and package qualifier.
// "*User" → "User", "pkg.User" → "User", "*pkg.User" → "User"
func normalizeGoTypeName(t string) string {
	t = strings.TrimPrefix(t, "*")
	if idx := strings.LastIndex(t, "."); idx >= 0 {
		t = t[idx+1:]
	}
	if t == "" || t[0] < 'A' || t[0] > 'Z' {
		return "" // skip built-in types like int, string, etc.
	}
	return t
}

// inferTypeFromGoExpr inspects a tree-sitter expression node to infer
// the type of a short variable declaration's RHS.
func inferTypeFromGoExpr(node *sitter.Node, src []byte, lang *sitter.Language) string {
	switch parser.NodeType(node, lang) {
	case "composite_literal":
		// User{} or User{field: val}
		// First named child is the type identifier.
		if node.NamedChildCount() > 0 {
			typeNode := node.NamedChild(0)
			return normalizeGoTypeName(typeNode.Text(src))
		}

	case "unary_expression":
		// &User{} — operand is composite_literal
		for i := 0; i < int(node.NamedChildCount()); i++ {
			child := node.NamedChild(i)
			if parser.NodeType(child, lang) == "composite_literal" {
				return inferTypeFromGoExpr(child, src, lang)
			}
		}

	case "call_expression":
		// NewUser() → "User" (Go constructor convention)
		if node.NamedChildCount() > 0 {
			funcNode := node.NamedChild(0)
			if parser.NodeType(funcNode, lang) == "identifier" {
				funcName := funcNode.Text(src)
				if strings.HasPrefix(funcName, "New") && len(funcName) > 3 {
					candidate := funcName[3:]
					if len(candidate) > 0 && candidate[0] >= 'A' && candidate[0] <= 'Z' {
						return candidate
					}
				}
			}
		}
	}

	return ""
}

// --- Tier 2: Chain resolution ---

// resolveChainType tries to infer the type of a chained expression like
// "svc.GetUser()" by looking up the root variable in the type env, then
// following method return types through already-extracted nodes.
func resolveChainType(expr string, tenv typeEnv, result *parser.ExtractionResult) string {
	// Strip balanced parentheses and their contents.
	// "svc.GetUser(arg1, arg2).Save()" → "svc.GetUser.Save"
	cleaned := stripCallArgs(expr)

	parts := strings.Split(cleaned, ".")
	if len(parts) < 2 {
		return ""
	}

	// Look up the root variable.
	currentType, ok := tenv[parts[0]]
	if !ok {
		return ""
	}

	// Walk the chain: for each segment, find a method on currentType and read return_type.
	for i := 1; i < len(parts); i++ {
		methodName := parts[i]
		returnType := findMethodReturnType(currentType, methodName, result)
		if returnType == "" {
			return "" // chain breaks
		}
		currentType = returnType
	}

	return currentType
}

// stripCallArgs removes balanced parenthesized argument lists from an expression.
// "svc.GetUser(arg1).Save()" → "svc.GetUser.Save"
func stripCallArgs(expr string) string {
	var b strings.Builder
	depth := 0
	for _, ch := range expr {
		switch ch {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(ch)
			}
		}
	}
	return b.String()
}

// findMethodReturnType searches extracted nodes for a method with the given
// receiver type and name, returning its Meta["return_type"] if found.
func findMethodReturnType(receiverType, methodName string, result *parser.ExtractionResult) string {
	for _, n := range result.Nodes {
		if n.Kind != graph.KindMethod && n.Kind != graph.KindFunction {
			continue
		}
		if n.Name != methodName {
			continue
		}
		if recv, ok := n.Meta["receiver"].(string); ok && recv == receiverType {
			if rt, ok := n.Meta["return_type"].(string); ok {
				return rt
			}
		}
	}
	return ""
}
