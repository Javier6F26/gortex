package languages

import (
	"path/filepath"
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// Shader covers GLSL, HLSL, and WGSL under one extractor — the
// syntaxes are close enough (C-like with `struct`, function signatures,
// `#include`) that splitting them buys nothing externally. We pick the
// grammar per file extension and fall back to GLSL for any unknown
// shader extension: GLSL is the most permissive and handles plain
// legacy shaders fine.
//
// Per-dialect nuances handled below:
//   - HLSL `cbuffer`/`tbuffer` declarations are surfaced by the
//     odvcencio HLSL grammar as `function_definition` nodes with a
//     `type_identifier` child whose text is `cbuffer`. We detect that
//     shape and emit them as KindType (matching the regex adapter).
//   - GLSL uniforms / in / out top-level `declaration` nodes get
//     promoted to KindVariable with the trailing identifier as the
//     name (matches the old `uModel` capture).
//   - WGSL doesn't have preproc includes; its `#`-free imports are
//     out of scope for now.

// ShaderExtractor extracts GLSL / HLSL / WGSL source using tree-sitter.
type ShaderExtractor struct {
	// langs maps a file extension (lowercased, leading dot) to the
	// tree-sitter language to use for that dialect.
	langs map[string]*sitter.Language
}

func NewShaderExtractor() *ShaderExtractor {
	glsl := grammars.GlslLanguage()
	hlsl := grammars.HlslLanguage()
	wgsl := grammars.WgslLanguage()
	langs := map[string]*sitter.Language{}
	for _, ext := range []string{".glsl", ".vert", ".frag", ".geom", ".comp", ".tesc", ".tese"} {
		langs[ext] = glsl
	}
	for _, ext := range []string{".hlsl", ".fx", ".fxh", ".hlsli", ".vsh", ".psh"} {
		langs[ext] = hlsl
	}
	// Reserved for future use: .wgsl is not in the original extensions
	// list so we never dispatch to it, but the grammar is wired up for
	// consistency with the migration plan and for ad-hoc use.
	langs[".wgsl"] = wgsl
	return &ShaderExtractor{langs: langs}
}

func (e *ShaderExtractor) Language() string { return "shader" }
func (e *ShaderExtractor) Extensions() []string {
	return []string{
		".glsl", ".vert", ".frag", ".geom", ".comp", ".tesc", ".tese",
		".hlsl", ".fx", ".fxh", ".hlsli", ".vsh", ".psh",
	}
}

// pickLang returns the tree-sitter language for the given file path.
// Unknown extensions default to GLSL — the most tolerant dialect.
func (e *ShaderExtractor) pickLang(filePath string) *sitter.Language {
	ext := strings.ToLower(filepath.Ext(filePath))
	if lang, ok := e.langs[ext]; ok {
		return lang
	}
	return grammars.GlslLanguage()
}

func (e *ShaderExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	lang := e.pickLang(filePath)
	tree, err := parser.ParseFile(src, lang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	root := tree.RootNode()
	lines := strings.Split(string(src), "\n")
	result := &parser.ExtractionResult{}

	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: len(lines),
		Language: "shader",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	add := func(name string, kind graph.NodeKind, start, end int) {
		if name == "" || isShaderKeyword(name) {
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
			Language: "shader",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}
	addImport := func(target string, line int) {
		if target == "" {
			return
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + target,
			Kind: graph.EdgeImports, FilePath: filePath, Line: line,
		})
	}

	walkNodes(root, func(node *sitter.Node) {
		switch parser.NodeType(node, lang) {
		case "function_definition":
			// HLSL parses `cbuffer X { ... }` as a function_definition
			// with a `type_identifier` child whose text is "cbuffer".
			// Detect and emit as a KindType instead.
			if name, isBuf := shaderConstantBufferName(node, src, lang); isBuf {
				add(name, graph.KindType,
					int(node.StartPoint().Row)+1, int(node.EndPoint().Row)+1)
				return
			}
			name := shaderFunctionName(node, src, lang)
			add(name, graph.KindFunction,
				int(node.StartPoint().Row)+1, int(node.EndPoint().Row)+1)
		case "struct_specifier", "struct_declaration":
			name := firstChildText(node, "type_identifier", src, lang)
			if name == "" {
				// WGSL uses `identifier` for the struct name.
				name = firstChildText(node, "identifier", src, lang)
			}
			if name == "" {
				return
			}
			add(name, graph.KindType,
				int(node.StartPoint().Row)+1, int(node.EndPoint().Row)+1)
		case "declaration":
			// Only promote storage-qualified top-level declarations
			// (uniform / in / out / attribute / varying / layout-…).
			// Plain local `float x;` declarations would otherwise flood
			// the index. We check for a storage qualifier child.
			if !shaderHasStorageQualifier(node, lang) {
				return
			}
			name := shaderDeclarationName(node, src, lang)
			if name == "" {
				return
			}
			start := int(node.StartPoint().Row) + 1
			add(name, graph.KindVariable, start, start)
		case "preproc_include":
			target := shaderIncludePath(node, src, lang)
			if target == "" {
				return
			}
			addImport(target, int(node.StartPoint().Row)+1)
		}
	})

	_ = lines
	return result, nil
}

// shaderFunctionName returns the identifier in a function_definition
// by reaching into its function_declarator child.
func shaderFunctionName(node *sitter.Node, src []byte, lang *sitter.Language) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, lang) == "function_declarator" {
			if name := firstChildText(c, "identifier", src, lang); name != "" {
				return name
			}
		}
	}
	return ""
}

// shaderConstantBufferName detects an HLSL `cbuffer Name { ... }` or
// `tbuffer Name { ... }` block misparsed as a function_definition by
// the grammar. Returns the buffer name and true when found.
func shaderConstantBufferName(node *sitter.Node, src []byte, lang *sitter.Language) (string, bool) {
	var typ, ident string
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		switch parser.NodeType(c, lang) {
		case "type_identifier":
			if typ == "" {
				typ = strings.TrimSpace(c.Text(src))
			}
		case "identifier":
			if ident == "" {
				ident = strings.TrimSpace(c.Text(src))
			}
		}
	}
	switch typ {
	case "cbuffer", "tbuffer":
		return ident, true
	}
	return "", false
}

// shaderHasStorageQualifier reports whether a top-level `declaration`
// carries one of the GLSL storage qualifiers we want to capture. The
// qualifiers appear as bare keyword children (`uniform`, `in`, …) or
// are preceded by a `layout_specification` node.
func shaderHasStorageQualifier(node *sitter.Node, lang *sitter.Language) bool {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		switch parser.NodeType(c, lang) {
		case "uniform", "in", "out", "attribute", "varying", "buffer",
			"layout_specification":
			return true
		}
	}
	return false
}

// shaderDeclarationName extracts the identifier from a top-level
// `declaration` node. The grammar lays out the children as
// [qualifier(s)] type_identifier identifier ;
func shaderDeclarationName(node *sitter.Node, src []byte, lang *sitter.Language) string {
	// The last `identifier` child is the variable name; earlier
	// identifiers might belong to `layout_specification` qualifiers.
	var last string
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, lang) == "identifier" {
			last = strings.TrimSpace(c.Text(src))
		}
	}
	return last
}

// shaderIncludePath extracts the include target from a
// preproc_include node. The path is a `string_literal` child with an
// inner `string_content` token we unwrap. `<system>` style includes
// surface through `system_lib_string` in some grammars.
func shaderIncludePath(node *sitter.Node, src []byte, lang *sitter.Language) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		switch parser.NodeType(c, lang) {
		case "string_literal":
			if inner := firstChildText(c, "string_content", src, lang); inner != "" {
				return inner
			}
			return strings.Trim(strings.TrimSpace(c.Text(src)), "\"")
		case "system_lib_string":
			return strings.Trim(strings.TrimSpace(c.Text(src)), "<>")
		}
	}
	return ""
}

// firstChildText returns the text of the first direct child whose
// grammar type matches `typ`.
func firstChildText(node *sitter.Node, typ string, src []byte, lang *sitter.Language) string {
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, lang) == typ {
			return strings.TrimSpace(c.Text(src))
		}
	}
	return ""
}

func isShaderKeyword(s string) bool {
	switch s {
	case "if", "else", "for", "while", "do", "switch", "case", "default",
		"break", "continue", "return", "discard", "void", "bool", "int",
		"uint", "float", "double", "vec2", "vec3", "vec4", "mat2", "mat3",
		"mat4", "sampler2D", "samplerCube", "struct", "uniform", "in", "out",
		"inout", "attribute", "varying", "const", "layout", "buffer":
		return true
	}
	return false
}

var _ parser.Extractor = (*ShaderExtractor)(nil)
