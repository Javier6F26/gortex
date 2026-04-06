package languages

import (
	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/ruby"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

const (
	qRbClass = `(class name: (constant) @class.name) @class.def`

	qRbModule = `(module name: (constant) @mod.name) @mod.def`

	qRbMethod = `(method name: (identifier) @method.name) @method.def`

	qRbCall = `(call method: (identifier) @call.name) @call.expr`

	qRbRequire = `(call
		method: (identifier) @req.method
		arguments: (argument_list
			(string (string_content) @req.path))) @req.def`

	qRbClassMethod = `(class
		name: (constant) @class.name
		body: (body_statement
			(method
				name: (identifier) @method.name) @method.def))`

	qRbAssignment = `(assignment
		left: (constant) @const.name
		right: (_) @const.value) @const.def`
)

// RubyExtractor extracts Ruby source files into graph nodes and edges.
type RubyExtractor struct {
	lang *sitter.Language
}

func NewRubyExtractor() *RubyExtractor {
	return &RubyExtractor{lang: ruby.GetLanguage()}
}

func (e *RubyExtractor) Language() string     { return "ruby" }
func (e *RubyExtractor) Extensions() []string { return []string{".rb", ".rake", ".gemspec"} }

func (e *RubyExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	tree, err := parser.ParseFile(src, e.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	root := tree.RootNode()
	result := &parser.ExtractionResult{}

	fileNode := &graph.Node{
		ID:        filePath,
		Kind:      graph.KindFile,
		Name:      filePath,
		FilePath:  filePath,
		StartLine: 1,
		EndLine:   int(root.EndPoint().Row) + 1,
		Language:  "ruby",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	methodLines := make(map[int]bool) // track lines already extracted as class methods

	// --- Modules ---
	matches, _ := parser.RunQuery(qRbModule, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["mod.name"].Text
		def := m.Captures["mod.def"]
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindPackage, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "ruby",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// --- Class methods (before top-level methods so we can skip them) ---
	matches, _ = parser.RunQuery(qRbClassMethod, e.lang, root, src)
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

		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindMethod, Name: methodName,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "ruby", Meta: map[string]any{
				"receiver":  className,
				"signature": "def " + methodName,
			},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
		typeID := filePath + "::" + className
		result.Edges = append(result.Edges, &graph.Edge{
			From: id, To: typeID, Kind: graph.EdgeMemberOf, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// --- Classes ---
	matches, _ = parser.RunQuery(qRbClass, e.lang, root, src)
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
			Language: "ruby",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// --- Top-level methods (skip lines already extracted as class methods) ---
	matches, _ = parser.RunQuery(qRbMethod, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["method.name"].Text
		def := m.Captures["method.def"]
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
			Language: "ruby", Meta: map[string]any{"signature": "def " + name},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// --- Imports (require / require_relative) ---
	matches, _ = parser.RunQuery(qRbRequire, e.lang, root, src)
	for _, m := range matches {
		method := m.Captures["req.method"].Text
		if method != "require" && method != "require_relative" {
			continue
		}
		path := m.Captures["req.path"]
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + path.Text,
			Kind: graph.EdgeImports, FilePath: filePath, Line: path.StartLine + 1,
		})
	}

	// --- Call sites ---
	funcRanges := buildFuncRanges(result)

	matches, _ = parser.RunQuery(qRbCall, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["call.name"].Text
		expr := m.Captures["call.expr"]
		// Skip require/require_relative — already handled as imports.
		if name == "require" || name == "require_relative" {
			continue
		}
		callerID := findEnclosingFunc(funcRanges, expr.StartLine+1)
		if callerID == "" {
			continue
		}

		// Check if the call has a receiver (obj.method style).
		callNode := expr.Node
		var target string
		if callNode != nil {
			receiver := callNode.ChildByFieldName("receiver")
			if receiver != nil {
				target = "unresolved::*." + name
			} else {
				target = "unresolved::" + name
			}
		} else {
			target = "unresolved::" + name
		}

		result.Edges = append(result.Edges, &graph.Edge{
			From: callerID, To: target,
			Kind: graph.EdgeCalls, FilePath: filePath, Line: expr.StartLine + 1,
		})
	}

	// --- Constants (uppercase assignments) ---
	matches, _ = parser.RunQuery(qRbAssignment, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["const.name"].Text
		def := m.Captures["const.def"]
		// Ruby constants start with an uppercase letter.
		if len(name) == 0 || !isUpperASCII(name[0]) {
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
			Language: "ruby",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	return result, nil
}

func isUpperASCII(b byte) bool {
	return b >= 'A' && b <= 'Z'
}

// Ensure RubyExtractor satisfies the Extractor interface at compile time.
var _ parser.Extractor = (*RubyExtractor)(nil)
