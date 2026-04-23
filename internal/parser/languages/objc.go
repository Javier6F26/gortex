package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// Objective-C / Objective-C++ is C-derived with `@` directives for class,
// protocol, and implementation declarations. Methods use a keyword-argument
// selector syntax: `- (ret)fooWith:(T)a andBar:(T)b`. The tree-sitter
// grammar exposes `class_interface`, `class_implementation`, and
// `protocol_declaration`; methods live as `method_declaration` (headers)
// or `method_definition` (with body). C-style functions reuse the C
// grammar's `function_definition`.
//
// This adapter is registered after the MATLAB one in register.go so the
// `.m` extension resolves here rather than to MATLAB.

// ObjCExtractor extracts Objective-C / Objective-C++ source with tree-sitter.
type ObjCExtractor struct {
	lang *sitter.Language
}

func NewObjCExtractor() *ObjCExtractor {
	return &ObjCExtractor{lang: grammars.ObjcLanguage()}
}

func (e *ObjCExtractor) Language() string     { return "objc" }
func (e *ObjCExtractor) Extensions() []string { return []string{".m", ".mm"} }

func (e *ObjCExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	tree, err := parser.ParseFile(src, e.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	root := tree.RootNode()
	result := &parser.ExtractionResult{}

	endLine := int(root.EndPoint().Row) + 1
	if endLine < 1 {
		endLine = 1
	}
	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: endLine,
		Language: "objc",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	add := func(name string, kind graph.NodeKind, start, end int) {
		if name == "" {
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
			Language: "objc",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	walkNodes(root, func(node *sitter.Node) {
		switch parser.NodeType(node, e.lang) {
		case "class_interface", "class_implementation":
			name := e.objcFirstIdent(node, src)
			add(name, graph.KindType,
				int(node.StartPoint().Row)+1, int(node.EndPoint().Row)+1)
		case "protocol_declaration":
			name := e.objcFirstIdent(node, src)
			add(name, graph.KindInterface,
				int(node.StartPoint().Row)+1, int(node.EndPoint().Row)+1)
		case "method_declaration", "method_definition":
			sel := e.objcBuildSelector(node, src)
			if sel == "" {
				return
			}
			add(sel, graph.KindMethod,
				int(node.StartPoint().Row)+1, int(node.EndPoint().Row)+1)
		case "function_definition":
			name := e.objcFunctionName(node, src)
			if name == "" || objcIsKeyword(name) {
				return
			}
			add(name, graph.KindFunction,
				int(node.StartPoint().Row)+1, int(node.EndPoint().Row)+1)
		case "preproc_include":
			e.objcHandlePreprocInclude(node, src, fileNode.ID, filePath, result)
		case "module_import":
			// `@import Foo.Bar;`
			mod := e.objcAtImportTarget(node, src)
			if mod != "" {
				result.Edges = append(result.Edges, &graph.Edge{
					From: fileNode.ID, To: "unresolved::import::" + mod,
					Kind: graph.EdgeImports, FilePath: filePath,
					Line: int(node.StartPoint().Row) + 1,
				})
			}
		}
	})

	return result, nil
}

// objcFirstIdent returns the first direct-child identifier's text.
func (e *ObjCExtractor) objcFirstIdent(node *sitter.Node, src []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, e.lang) == "identifier" {
			return c.Text(src)
		}
	}
	return ""
}

// objcFunctionName pulls the name from a C-style function definition:
// `function_definition > function_declarator > identifier`.
func (e *ObjCExtractor) objcFunctionName(node *sitter.Node, src []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, e.lang) != "function_declarator" {
			continue
		}
		for j := 0; j < int(c.ChildCount()); j++ {
			gc := c.Child(j)
			if gc == nil {
				continue
			}
			if parser.NodeType(gc, e.lang) == "identifier" {
				return gc.Text(src)
			}
		}
	}
	return ""
}

// objcBuildSelector reconstructs the canonical Objective-C selector from a
// method_declaration / method_definition. Unary selectors are a single
// identifier without a colon (`viewDidLoad`). Keyword selectors are a
// sequence of `identifier method_parameter...` — each method_parameter
// begins with `:` and contributes one colon to the selector.
func (e *ObjCExtractor) objcBuildSelector(node *sitter.Node, src []byte) string {
	var (
		parts    []string
		paramCnt int
	)
	sawType := false // skip the leading `method_type` (return type)
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		switch parser.NodeType(c, e.lang) {
		case "method_type":
			sawType = true
		case "identifier":
			if sawType {
				parts = append(parts, c.Text(src))
			}
		case "method_parameter":
			paramCnt++
		}
	}
	if len(parts) == 0 {
		return ""
	}
	if paramCnt == 0 {
		// Unary selector.
		return parts[0]
	}
	var b strings.Builder
	for i := 0; i < len(parts); i++ {
		b.WriteString(parts[i])
		if i < paramCnt {
			b.WriteByte(':')
		}
	}
	return b.String()
}

// objcHandlePreprocInclude covers `#import <Foo/Bar.h>` (system_lib_string)
// and `#import "Foo.h"` (string_literal).
func (e *ObjCExtractor) objcHandlePreprocInclude(
	node *sitter.Node, src []byte, fileID, filePath string,
	result *parser.ExtractionResult,
) {
	line := int(node.StartPoint().Row) + 1
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		switch parser.NodeType(c, e.lang) {
		case "system_lib_string":
			raw := strings.TrimSpace(c.Text(src))
			target := strings.Trim(raw, "<>")
			if target == "" {
				continue
			}
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileID, To: "unresolved::import::" + target,
				Kind: graph.EdgeImports, FilePath: filePath, Line: line,
			})
		case "string_literal":
			// Extract inner content.
			target := ""
			for j := 0; j < int(c.ChildCount()); j++ {
				gc := c.Child(j)
				if gc == nil {
					continue
				}
				if parser.NodeType(gc, e.lang) == "string_content" {
					target = gc.Text(src)
					break
				}
			}
			if target == "" {
				target = strings.Trim(c.Text(src), `"`)
			}
			if target == "" {
				continue
			}
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileID, To: "unresolved::import::" + target,
				Kind: graph.EdgeImports, FilePath: filePath, Line: line,
			})
		}
	}
}

// objcAtImportTarget returns the dotted module name from `@import A.B;`.
func (e *ObjCExtractor) objcAtImportTarget(node *sitter.Node, src []byte) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, e.lang) == "identifier" ||
			parser.NodeType(c, e.lang) == "module_name" {
			return strings.TrimSpace(c.Text(src))
		}
	}
	// Fallback: first identifier anywhere under the node.
	return firstIdentifier(node, src, e.lang)
}

func objcIsKeyword(s string) bool {
	switch s {
	case "if", "else", "while", "for", "do", "switch", "case", "default",
		"return", "break", "continue", "sizeof", "typedef", "struct",
		"enum", "union", "static", "extern", "inline", "const", "void":
		return true
	}
	return false
}

var _ parser.Extractor = (*ObjCExtractor)(nil)
