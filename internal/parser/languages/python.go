package languages

import (
	"strings"
	"unicode"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

const (
	pyQFunction = `(function_definition
		name: (identifier) @func.name) @func.def`

	pyQClass = `(class_definition
		name: (identifier) @class.name) @class.def`

	pyQImport = `(import_statement
		name: (dotted_name) @import.name) @import.def`

	pyQImportFrom = `(import_from_statement
		module_name: (dotted_name) @import.module) @import.def`

	pyQCall = `(call
		function: (identifier) @call.name) @call.expr`

	pyQCallAttr = `(call
		function: (attribute
			object: (_) @call.receiver
			attribute: (identifier) @call.method)) @call.expr`

	pyQAssignment = `(assignment
		left: (identifier) @var.name) @var.def`

	// Tier 0: typed assignment — x: Type = expr
	pyQTypedAssignment = `(assignment
		left: (identifier) @tvar.name
		type: (type (identifier) @tvar.type)) @tvar.def`

	// Tier 1: untyped assignment — x = expr (for constructor inference)
	pyQUntypedAssignment = `(assignment
		left: (identifier) @uvar.name
		right: (call
			function: (identifier) @uvar.callee)) @uvar.def`

	pyQClassMethod = `(class_definition
		name: (identifier) @class.name
		body: (block
			(function_definition
				name: (identifier) @method.name) @method.def))`
)

// PythonExtractor extracts Python source files.
type PythonExtractor struct {
	lang *sitter.Language
}

func NewPythonExtractor() *PythonExtractor {
	return &PythonExtractor{lang: grammars.PythonLanguage()}
}

func (e *PythonExtractor) Language() string     { return "python" }
func (e *PythonExtractor) Extensions() []string { return []string{".py"} }

func (e *PythonExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "python",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	methodLines := make(map[int]bool) // track lines already extracted as methods

	// Class methods — extract before functions so we can skip them.
	matches, _ := parser.RunQuery(pyQClassMethod, e.lang, root, src)
	for _, m := range matches {
		className := m.Captures["class.name"].Text
		methodName := m.Captures["method.name"].Text
		def := m.Captures["method.def"]

		id := filePath + "::" + className + "." + methodName
		if seen[id] {
			continue
		}
		seen[id] = true
		methodLines[def.StartLine] = true

		node := &graph.Node{
			ID: id, Kind: graph.KindMethod, Name: methodName,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "python", Meta: map[string]any{
				"receiver":  className,
				"signature": "def " + methodName + "(...)",
			},
		}
		// Extract return type hint from the function_definition node.
		if def.Node != nil {
			if rt := extractPyReturnType(def.Node, src, e.lang); rt != "" {
				node.Meta["return_type"] = rt
			}
		}
		result.Nodes = append(result.Nodes, node)
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
		typeID := filePath + "::" + className
		result.Edges = append(result.Edges, &graph.Edge{
			From: id, To: typeID, Kind: graph.EdgeMemberOf, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Functions (top-level only — skip lines already extracted as methods).
	matches, _ = parser.RunQuery(pyQFunction, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["func.name"].Text
		def := m.Captures["func.def"]
		if methodLines[def.StartLine] {
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
			Language: "python", Meta: map[string]any{"signature": "def " + name + "(...)"},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Classes.
	matches, _ = parser.RunQuery(pyQClass, e.lang, root, src)
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
			Language: "python",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Imports. `imports` is a per-file alias→module map used by the
	// selector-call pass below:
	//
	//   import os                  → os → os
	//   import os.path             → os → os.path (root ident is bound)
	//   import numpy as np         → np → numpy
	//   from os import path        → not mapped (bare binding)
	//
	// `from X import Y as Z` isn't a namespace handle — Z is called
	// directly (pyQCall catches it), not as `Z.foo()` — so we skip it.
	imports := map[string]string{}
	matches, _ = parser.RunQuery(pyQImport, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["import.name"]
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + name.Text,
			Kind: graph.EdgeImports, FilePath: filePath, Line: name.StartLine + 1,
		})
		// Walk the import_statement to handle `as` aliases. The grammar
		// emits either a bare dotted_name or an aliased_import(name,
		// alias) node as children of the import_statement.
		def, ok := m.Captures["import.def"]
		if !ok || def.Node == nil {
			continue
		}
		stmt := def.Node
		for i := 0; i < int(stmt.NamedChildCount()); i++ {
			child := stmt.NamedChild(i)
			switch parser.NodeType(child, e.lang) {
			case "dotted_name":
				dotted := child.Text(src)
				alias := dotted
				if j := strings.Index(dotted, "."); j >= 0 {
					alias = dotted[:j] // `import os.path` binds `os`
				}
				imports[alias] = dotted
			case "aliased_import":
				var modulePath, alias string
				for j := 0; j < int(child.NamedChildCount()); j++ {
					cc := child.NamedChild(j)
					switch parser.NodeType(cc, e.lang) {
					case "dotted_name":
						modulePath = cc.Text(src)
					case "identifier":
						alias = cc.Text(src)
					}
				}
				if alias != "" && modulePath != "" {
					imports[alias] = modulePath
				}
			}
		}
	}
	matches, _ = parser.RunQuery(pyQImportFrom, e.lang, root, src)
	for _, m := range matches {
		mod := m.Captures["import.module"]
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + mod.Text,
			Kind: graph.EdgeImports, FilePath: filePath, Line: mod.StartLine + 1,
		})
	}

	// Build type environment before extracting calls.
	tenv := e.buildTypeEnv(root, src)

	// Call sites.
	funcRanges := buildFuncRanges(result)

	matches, _ = parser.RunQuery(pyQCall, e.lang, root, src)
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

		// FastAPI dependency injection: `Depends(target)` appears in a
		// parameter default or inside `Annotated[T, Depends(target)]`,
		// and the usual call-edge path only links the caller to
		// `Depends`, not to the target. Without this pass,
		// find_usages(target) for DI-injected factories returns empty
		// even though the target is exactly what the handler ends up
		// invoking. Emit a direct edge from the enclosing function to
		// the first identifier argument of Depends so the target shows
		// up as a caller relationship.
		if name == "Depends" && expr.Node != nil {
			if dep := firstIdentifierArg(expr.Node, src, e.lang); dep != "" {
				result.Edges = append(result.Edges, &graph.Edge{
					From: callerID, To: "unresolved::" + dep,
					Kind: graph.EdgeCalls, FilePath: filePath, Line: expr.StartLine + 1,
					Meta: map[string]any{"via": "fastapi.Depends"},
				})
			}
		}
	}

	matches, _ = parser.RunQuery(pyQCallAttr, e.lang, root, src)
	for _, m := range matches {
		method := m.Captures["call.method"].Text
		receiverText := m.Captures["call.receiver"].Text
		expr := m.Captures["call.expr"]
		callerID := findEnclosingFunc(funcRanges, expr.StartLine+1)
		if callerID == "" {
			continue
		}

		// Module-qualified call (requests.get, np.array, os.path.join):
		// attach the import path so resolver can classify externally.
		// Handles both `foo.method` and `foo.bar.method` by walking the
		// dotted receiver — Python's `import os.path` binds `os`, so
		// `os.path.join(...)` has receiver `os.path` but alias `os`.
		if importPath, ok := lookupPyImport(receiverText, imports); ok {
			result.Edges = append(result.Edges, &graph.Edge{
				From: callerID, To: "unresolved::extern::" + importPath + "::" + method,
				Kind: graph.EdgeCalls, FilePath: filePath, Line: expr.StartLine + 1,
			})
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

	// Module-level variables (simple assignments at top level).
	matches, _ = parser.RunQuery(pyQAssignment, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["var.name"].Text
		def := m.Captures["var.def"]
		// Only top-level: parent is module.
		if def.Node != nil && def.Node.Parent() != nil && parser.NodeType(def.Node.Parent(), e.lang) == "module" {
			id := filePath + "::" + name
			if seen[id] || strings.HasPrefix(name, "_") {
				continue
			}
			seen[id] = true
			result.Nodes = append(result.Nodes, &graph.Node{
				ID: id, Kind: graph.KindVariable, Name: name,
				FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
				Language: "python",
			})
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
			})
		}
	}

	return result, nil
}

// buildTypeEnv scans Python assignments for type annotations (Tier 0)
// and class-constructor calls (Tier 1) to build a variable->type map.
func (e *PythonExtractor) buildTypeEnv(root *sitter.Node, src []byte) typeEnv {
	tenv := make(typeEnv)

	// Tier 0: explicit type hints — x: Type = expr
	matches, _ := parser.RunQuery(pyQTypedAssignment, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["tvar.name"].Text
		typeName := normalizePyTypeName(m.Captures["tvar.type"].Text)
		if typeName != "" {
			tenv[name] = typeName
		}
	}

	// Tier 1: constructor calls — x = ClassName(...)
	// Convention: class names start with an uppercase letter.
	matches, _ = parser.RunQuery(pyQUntypedAssignment, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["uvar.name"].Text
		if _, exists := tenv[name]; exists {
			continue // Tier 0 already resolved
		}
		callee := m.Captures["uvar.callee"].Text
		if callee != "" && unicode.IsUpper(rune(callee[0])) {
			tenv[name] = callee
		}
	}

	return tenv
}

// firstIdentifierArg returns the string content of the first positional
// argument to a Python call_expression when that argument is a bare
// identifier (function name or class name), or "" otherwise. Used for
// FastAPI's Depends(target) where we want the target, not Depends
// itself, to show up as the called symbol. Non-identifier arguments —
// lambdas, attribute access, calls — are skipped because they can't
// be statically resolved to a graph node.
func firstIdentifierArg(callNode *sitter.Node, src []byte, lang *sitter.Language) string {
	args := callNode.ChildByFieldName("arguments", lang)
	if args == nil {
		return ""
	}
	for i := 0; i < int(args.NamedChildCount()); i++ {
		arg := args.NamedChild(i)
		if arg == nil {
			continue
		}
		// Skip keyword arguments — Depends(use_cache=False, ...) shouldn't
		// produce a call edge to `False`.
		if parser.NodeType(arg, lang) == "keyword_argument" {
			continue
		}
		if parser.NodeType(arg, lang) == "identifier" {
			return arg.Text(src)
		}
		return ""
	}
	return ""
}

// extractPyReturnType walks a function_definition node for a return_type child
// (the `-> Type` annotation) and returns the normalized type name.
func extractPyReturnType(funcNode *sitter.Node, src []byte, lang *sitter.Language) string {
	for i := 0; i < int(funcNode.NamedChildCount()); i++ {
		child := funcNode.NamedChild(i)
		if parser.NodeType(child, lang) == "type" {
			// Check if preceding sibling token is "->".
			// In tree-sitter Python grammar, the return type is a "type" child
			// that appears after the parameters.
			return normalizePyTypeName(child.Text(src))
		}
	}
	return ""
}

// normalizePyTypeName strips Optional[], List[], etc. and skips builtins.
func normalizePyTypeName(t string) string {
	t = strings.TrimSpace(t)
	// Strip Optional[...], List[...], etc.
	if idx := strings.Index(t, "["); idx > 0 {
		t = t[:idx]
	}
	switch t {
	case "int", "float", "str", "bool", "bytes", "None", "list", "dict", "set", "tuple", "object":
		return ""
	}
	if t == "" || (t[0] >= 'a' && t[0] <= 'z') {
		return ""
	}
	return t
}

// lookupPyImport resolves a dotted Python receiver to an import path.
// Tries the full receiver first, then progressively shorter prefixes
// (os.path → os), so `os.path.join(x)` with `import os.path` finds the
// right module.
func lookupPyImport(receiver string, imports map[string]string) (string, bool) {
	if p, ok := imports[receiver]; ok {
		return p, true
	}
	for i := strings.LastIndex(receiver, "."); i > 0; i = strings.LastIndex(receiver[:i], ".") {
		prefix := receiver[:i]
		if p, ok := imports[prefix]; ok {
			return p, true
		}
	}
	if i := strings.Index(receiver, "."); i > 0 {
		if p, ok := imports[receiver[:i]]; ok {
			return p, true
		}
	}
	return "", false
}
