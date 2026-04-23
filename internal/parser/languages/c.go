package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// Tree-sitter query patterns for C source files.
const (
	qCFunction = `(function_definition
		declarator: (function_declarator
			declarator: (identifier) @func.name)) @func.def`

	qCStruct = `(struct_specifier
		name: (type_identifier) @struct.name) @struct.def`

	qCEnum = `(enum_specifier
		name: (type_identifier) @enum.name) @enum.def`

	qCInclude = `(preproc_include
		path: (_) @include.path) @include.def`

	qCCall = `(call_expression
		function: (identifier) @call.name) @call.expr`

	qCTypedef = `(type_definition
		declarator: (type_identifier) @typedef.name) @typedef.def`

	qCDeclaration = `(declaration
		declarator: (init_declarator
			declarator: (identifier) @var.name)) @var.def`

	qCFunctionProto = `(declaration
		declarator: (function_declarator
			declarator: (identifier) @proto.name)) @proto.def`
)

// CExtractor extracts C source files into graph nodes and edges.
type CExtractor struct {
	lang *sitter.Language
}

func NewCExtractor() *CExtractor {
	return &CExtractor{lang: grammars.CLanguage()}
}

func (e *CExtractor) Language() string     { return "c" }
func (e *CExtractor) Extensions() []string { return []string{".c", ".h"} }

func (e *CExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "c",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)

	// Functions.
	e.extractFunctions(root, src, filePath, fileNode.ID, result, seen)

	// Function prototypes (declarations).
	e.extractPrototypes(root, src, filePath, fileNode.ID, result, seen)

	// Structs.
	e.extractStructs(root, src, filePath, fileNode.ID, result, seen)

	// Enums.
	e.extractEnums(root, src, filePath, fileNode.ID, result, seen)

	// Typedefs.
	e.extractTypedefs(root, src, filePath, fileNode.ID, result, seen)

	// Includes.
	e.extractIncludes(root, src, filePath, fileNode.ID, result)

	// Call sites.
	e.extractCalls(root, src, filePath, result)

	// Global variables.
	e.extractGlobals(root, src, filePath, fileNode.ID, result, seen)

	return result, nil
}

func (e *CExtractor) extractFunctions(root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult, seen map[string]bool) {
	matches, err := parser.RunQuery(qCFunction, e.lang, root, src)
	if err != nil {
		return
	}
	for _, m := range matches {
		name := m.Captures["func.name"].Text
		def := m.Captures["func.def"]
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindFunction, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "c", Meta: map[string]any{
				"signature": strings.TrimSpace(extractCSignature(def.Node, src)),
			},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}
}

func (e *CExtractor) extractPrototypes(root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult, seen map[string]bool) {
	matches, err := parser.RunQuery(qCFunctionProto, e.lang, root, src)
	if err != nil {
		return
	}
	for _, m := range matches {
		name := m.Captures["proto.name"].Text
		def := m.Captures["proto.def"]
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindFunction, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "c", Meta: map[string]any{
				"signature": strings.TrimSuffix(strings.TrimSpace(def.Text), ";"),
				"prototype": true,
			},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}
}

func (e *CExtractor) extractStructs(root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult, seen map[string]bool) {
	matches, err := parser.RunQuery(qCStruct, e.lang, root, src)
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
			Language: "c",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}
}

func (e *CExtractor) extractEnums(root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult, seen map[string]bool) {
	matches, err := parser.RunQuery(qCEnum, e.lang, root, src)
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
			Language: "c",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}
}

func (e *CExtractor) extractTypedefs(root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult, seen map[string]bool) {
	matches, err := parser.RunQuery(qCTypedef, e.lang, root, src)
	if err != nil {
		return
	}
	for _, m := range matches {
		name := m.Captures["typedef.name"].Text
		def := m.Captures["typedef.def"]
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindType, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "c",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}
}

func (e *CExtractor) extractIncludes(root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult) {
	matches, err := parser.RunQuery(qCInclude, e.lang, root, src)
	if err != nil {
		return
	}
	for _, m := range matches {
		pathCap := m.Captures["include.path"]
		// Strip quotes or angle brackets: "foo.h" -> foo.h, <stdio.h> -> stdio.h
		includePath := pathCap.Text
		includePath = strings.Trim(includePath, `"`)
		includePath = strings.Trim(includePath, "<>")
		result.Edges = append(result.Edges, &graph.Edge{
			From:     fileID,
			To:       "unresolved::import::" + includePath,
			Kind:     graph.EdgeImports,
			FilePath: filePath,
			Line:     pathCap.StartLine + 1,
		})
	}
}

func (e *CExtractor) extractCalls(root *sitter.Node, src []byte, filePath string, result *parser.ExtractionResult) {
	funcRanges := buildFuncRanges(result)

	matches, _ := parser.RunQuery(qCCall, e.lang, root, src)
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
}

func (e *CExtractor) extractGlobals(root *sitter.Node, src []byte, filePath, fileID string, result *parser.ExtractionResult, seen map[string]bool) {
	matches, err := parser.RunQuery(qCDeclaration, e.lang, root, src)
	if err != nil {
		return
	}
	// Build a set of function start/end lines to skip local variables.
	funcRanges := buildFuncRanges(result)

	for _, m := range matches {
		name := m.Captures["var.name"].Text
		def := m.Captures["var.def"]

		// Only keep top-level declarations (not inside functions).
		if findEnclosingFunc(funcRanges, def.StartLine+1) != "" {
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
			Language: "c",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}
}

// extractCSignature extracts a function signature from its definition node.
// It takes the text up to (but not including) the compound_statement body.
func extractCSignature(node *sitter.Node, src []byte) string {
	fullText := node.Text(src)
	// Find the opening brace of the function body and trim there.
	idx := strings.Index(fullText, "{")
	if idx > 0 {
		return strings.TrimSpace(fullText[:idx])
	}
	return fullText
}
