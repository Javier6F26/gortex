package languages

import (
	"regexp"
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// ERB embeds Ruby inside `<% ... %>` tags. The extractor captures Ruby
// `def name` and `class Name` declarations inside those blocks as
// function / type nodes, and Rails-style `render 'partial'` directives
// as import edges. The regexes match both the plain form and the
// symbol-hash form (`render :partial => 'x'`).
//
// Parsing strategy:
//   - Use EmbeddedTemplateLanguage to walk the template AST and locate
//     each `code` span (inside `directive` / `output_directive`).
//   - Re-parse each code span with RubyLanguage to identify class / def
//     declarations using the Ruby AST directly.
//   - Render-call imports are detected with a narrow regex over each
//     code span: the Ruby AST for `render :partial => 'x'` is expensive
//     to traverse for a one-off hash literal, and the regex has zero
//     false positives inside `code` spans.

// narrowRegexFallback: render-directive argument matching inside a
// code span. Kept here (not at package level) because it is an
// adapter-local fallback for the symbol-hash form.
var (
	erbRenderHashRe = regexp.MustCompile(`render\s*\(?\s*:partial\s*=>\s*['"]([^'"]+)['"]`)
	erbRenderStrRe  = regexp.MustCompile(`render\s*\(?\s*['"]([^'"]+)['"]`)
)

// ERBExtractor extracts ERB templates into graph nodes and edges.
type ERBExtractor struct {
	lang     *sitter.Language
	rubyLang *sitter.Language
}

func NewERBExtractor() *ERBExtractor {
	return &ERBExtractor{
		lang:     grammars.EmbeddedTemplateLanguage(),
		rubyLang: grammars.RubyLanguage(),
	}
}

func (e *ERBExtractor) Language() string { return "erb" }
func (e *ERBExtractor) Extensions() []string {
	return []string{".erb", ".rhtml", ".html.erb", ".js.erb", ".css.erb", ".json.erb"}
}

func (e *ERBExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
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
		Language: "erb",
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
			Language: "erb",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	type importKey struct {
		mod  string
		line int
	}
	importSeen := make(map[importKey]bool)
	emitImport := func(mod string, line int) {
		k := importKey{mod: mod, line: line}
		if importSeen[k] {
			return
		}
		importSeen[k] = true
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + mod,
			Kind: graph.EdgeImports, FilePath: filePath, Line: line,
		})
	}

	// Walk the template AST and collect each `code` span.
	walkNodes(root, func(node *sitter.Node) {
		if parser.NodeType(node, e.lang) != "code" {
			return
		}
		codeText := node.Text(src)
		// Row offset of the code span's start within the original file.
		rowOffset := int(node.StartPoint().Row)
		colOffset := int(node.StartPoint().Column)

		e.extractRubySymbols(codeText, rowOffset, colOffset, add)
		e.extractRenderImports(codeText, rowOffset, emitImport)
	})

	return result, nil
}

// extractRubySymbols parses a code span as Ruby and adds class / def
// symbols with start/end lines adjusted back to the original file.
func (e *ERBExtractor) extractRubySymbols(
	code string, rowOffset, colOffset int,
	add func(name string, kind graph.NodeKind, start, end int),
) {
	// Re-parse the code span on its own. Tree-sitter's point-based lines
	// are local to the span input; add rowOffset to get file lines.
	srcBytes := []byte(code)
	rtree, err := parser.ParseFile(srcBytes, e.rubyLang)
	if err != nil || rtree == nil {
		return
	}
	defer rtree.Close()

	root := rtree.RootNode()
	walkNodes(root, func(node *sitter.Node) {
		t := parser.NodeType(node, e.rubyLang)
		startLine := int(node.StartPoint().Row) + 1 + rowOffset
		endLine := int(node.EndPoint().Row) + 1 + rowOffset

		switch t {
		case "class":
			// First constant child is the class name.
			for i := 0; i < int(node.ChildCount()); i++ {
				child := node.Child(i)
				if child != nil && parser.NodeType(child, e.rubyLang) == "constant" {
					name := child.Text(srcBytes)
					add(name, graph.KindType, startLine, endLine)
					break
				}
			}
		case "method":
			// (method def identifier body end) — find the identifier child.
			for i := 0; i < int(node.ChildCount()); i++ {
				child := node.Child(i)
				if child != nil && parser.NodeType(child, e.rubyLang) == "identifier" {
					name := child.Text(srcBytes)
					add(name, graph.KindFunction, startLine, endLine)
					break
				}
			}
		}
	})
	_ = colOffset // reserved for future column-aware mapping
}

// extractRenderImports scans a code span for `render 'x'` /
// `render :partial => 'x'` and emits import edges. Dedup by (mod,line).
func (e *ERBExtractor) extractRenderImports(
	code string, rowOffset int,
	emitImport func(mod string, line int),
) {
	src := []byte(code)
	// Hash form first so its string-literal isn't also picked up by the
	// looser plain form.
	for _, m := range erbRenderHashRe.FindAllSubmatchIndex(src, -1) {
		mod := string(src[m[2]:m[3]])
		line := rowOffset + 1 + strings.Count(code[:m[0]], "\n")
		emitImport(mod, line)
	}
	for _, m := range erbRenderStrRe.FindAllSubmatchIndex(src, -1) {
		mod := string(src[m[2]:m[3]])
		line := rowOffset + 1 + strings.Count(code[:m[0]], "\n")
		emitImport(mod, line)
	}
}

var _ parser.Extractor = (*ERBExtractor)(nil)
