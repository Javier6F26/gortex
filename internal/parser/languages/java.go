package languages

import (
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/java"
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
)

// JavaExtractor extracts Java source files.
type JavaExtractor struct {
	lang *sitter.Language
}

func NewJavaExtractor() *JavaExtractor {
	return &JavaExtractor{lang: java.GetLanguage()}
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
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindMethod, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "java",
			Meta:     map[string]any{"receiver": className},
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
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindMethod, Name: className + ".<init>",
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "java",
			Meta:     map[string]any{"receiver": className},
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

	// Call sites.
	funcRanges := buildFuncRanges(result)
	matches, _ = parser.RunQuery(javaQCall, e.lang, root, src)
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

	return result, nil
}
