package languages

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/rust"
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
			field: (field_identifier) @call.method)) @call.expr`

	rsQConst = `(const_item
		name: (identifier) @const.name) @const.def`

	rsQStatic = `(static_item
		name: (identifier) @static.name) @static.def`
)

// RustExtractor extracts Rust source files.
type RustExtractor struct {
	lang *sitter.Language
}

func NewRustExtractor() *RustExtractor {
	return &RustExtractor{lang: rust.GetLanguage()}
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
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindMethod, Name: methodName,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "rust", Meta: map[string]any{
				"receiver":  typeName,
				"signature": "fn " + methodName + "(...)",
			},
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
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
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

	// Call sites.
	funcRanges := buildFuncRanges(result)

	for _, q := range []string{rsQCall, rsQCallPath} {
		matches, _ = parser.RunQuery(q, e.lang, root, src)
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

	matches, _ = parser.RunQuery(rsQMethodCall, e.lang, root, src)
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
