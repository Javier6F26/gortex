package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// ApexExtractor extracts Salesforce Apex source files using tree-sitter.
//
// Apex is Java-ish OO plus Salesforce-specific trigger declarations.
// The grammar exposes:
//
//   - class_declaration      → KindType
//   - interface_declaration  → KindInterface
//   - enum_declaration       → KindType
//   - trigger_declaration    → KindType  (Salesforce-specific)
//   - method_declaration     → KindMethod
//   - method_invocation      → EdgeCalls from enclosing method
//
// Apex has no user-visible import statement, so no import edges are
// emitted.
type ApexExtractor struct {
	lang *sitter.Language
}

func NewApexExtractor() *ApexExtractor {
	return &ApexExtractor{lang: grammars.ApexLanguage()}
}

func (e *ApexExtractor) Language() string     { return "apex" }
func (e *ApexExtractor) Extensions() []string { return []string{".cls", ".trigger", ".apex"} }

func (e *ApexExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "apex",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	add := func(name string, kind graph.NodeKind, start, end int) {
		if name == "" || isApexKeyword(strings.ToLower(name)) {
			return
		}
		id := filePath + "::" + name
		if seen[id] {
			return
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: kind, Name: name,
			FilePath: filePath, StartLine: start, EndLine: end,
			Language: "apex",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	// Collect declarations via a full walk so nested methods inside
	// classes/interfaces/triggers are captured regardless of depth.
	walkNodes(root, func(n *sitter.Node) {
		if n == nil {
			return
		}
		start := int(n.StartPoint().Row) + 1
		end := int(n.EndPoint().Row) + 1

		switch parser.NodeType(n, e.lang) {
		case "class_declaration":
			add(apexChildIdentifier(n, src, e.lang), graph.KindType, start, end)
		case "interface_declaration":
			add(apexChildIdentifier(n, src, e.lang), graph.KindInterface, start, end)
		case "enum_declaration":
			add(apexChildIdentifier(n, src, e.lang), graph.KindType, start, end)
		case "trigger_declaration":
			add(apexChildIdentifier(n, src, e.lang), graph.KindType, start, end)
		case "method_declaration":
			add(apexChildIdentifier(n, src, e.lang), graph.KindMethod, start, end)
		}
	})

	// Call edges: method_invocation carries the callee on its last
	// identifier child; walk once more to attribute each call to the
	// enclosing method.
	funcRanges := buildFuncRanges(result)
	walkNodes(root, func(n *sitter.Node) {
		if n == nil {
			return
		}
		if parser.NodeType(n, e.lang) != "method_invocation" {
			return
		}
		name := apexMethodInvocationName(n, src, e.lang)
		if name == "" || isApexKeyword(strings.ToLower(name)) {
			return
		}
		line := int(n.StartPoint().Row) + 1
		callerID := findEnclosingFunc(funcRanges, line)
		if callerID == "" || strings.HasSuffix(callerID, "::"+name) {
			return
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: callerID, To: "unresolved::" + name,
			Kind: graph.EdgeCalls, FilePath: filePath, Line: line,
		})
	})

	return result, nil
}

// apexChildIdentifier returns the text of the first direct `identifier`
// child of a declaration node (class/interface/enum/trigger/method all
// share this shape — modifiers and types come first, then the identifier).
func apexChildIdentifier(node *sitter.Node, src []byte, lang *sitter.Language) string {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child == nil {
			continue
		}
		if parser.NodeType(child, lang) == "identifier" {
			return child.Text(src)
		}
	}
	return ""
}

// apexMethodInvocationName extracts the callee name from a
// method_invocation node. The grammar renders `Foo.bar(x)` as a
// method_invocation with two identifier children — the receiver then
// the method — plus an argument_list; bare `bar(x)` has a single
// identifier child.
func apexMethodInvocationName(node *sitter.Node, src []byte, lang *sitter.Language) string {
	var last string
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child == nil {
			continue
		}
		if parser.NodeType(child, lang) == "identifier" {
			last = child.Text(src)
		}
	}
	return last
}

func isApexKeyword(s string) bool {
	switch s {
	case "if", "else", "for", "while", "do", "switch", "when", "break",
		"continue", "return", "new", "this", "super", "null", "true",
		"false", "try", "catch", "finally", "throw", "class", "interface",
		"enum", "trigger", "extends", "implements", "public", "private",
		"protected", "global", "static", "virtual", "abstract", "override",
		"final", "transient", "testmethod", "webservice", "with", "without",
		"inherited", "sharing", "on", "void", "instanceof":
		return true
	}
	return false
}

var _ parser.Extractor = (*ApexExtractor)(nil)
