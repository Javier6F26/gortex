package languages

import (
	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// ClojureExtractor extracts Clojure source files.
//
// The odvcencio Clojure grammar emits `list_lit` for parenthesised
// forms; the first named child is the head `sym_lit`. We pattern-match
// on that head (`defn`, `defn-`, `defmacro`, `defrecord`, `deftype`,
// `defprotocol`, `ns`, `def`) and pull the following `sym_lit` as the
// symbol name.
type ClojureExtractor struct {
	lang *sitter.Language
}

func NewClojureExtractor() *ClojureExtractor {
	return &ClojureExtractor{lang: grammars.ClojureLanguage()}
}

func (e *ClojureExtractor) Language() string     { return "clojure" }
func (e *ClojureExtractor) Extensions() []string { return []string{".clj", ".cljs", ".cljc", ".edn"} }

func (e *ClojureExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	result := &parser.ExtractionResult{}

	tree, err := parser.ParseFile(src, e.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()
	root := tree.RootNode()

	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: int(root.EndPoint().Row) + 1,
		Language: "clojure",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	addDef := func(name string, kind graph.NodeKind, start, end int, meta map[string]any) {
		if name == "" {
			return
		}
		id := filePath + "::" + name
		if seen[id] {
			return
		}
		seen[id] = true
		n := &graph.Node{
			ID: id, Kind: kind, Name: name,
			FilePath: filePath, StartLine: start, EndLine: end,
			Language: "clojure",
		}
		if meta != nil {
			n.Meta = meta
		}
		result.Nodes = append(result.Nodes, n)
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	// Walk top-level list_lit forms. Clojure's special forms are all
	// list-shaped, so a single pass is enough.
	var nsName string
	walkNodes(root, func(n *sitter.Node) {
		if n == nil || parser.NodeType(n, e.lang) != "list_lit" {
			return
		}
		head := clojureListHead(n, src, e.lang)
		switch head {
		case "ns":
			// (ns my.app (:require …) (:import …))
			nsSym := clojureNthSym(n, 1, src, e.lang)
			if nsSym != "" {
				nsName = nsSym
				start := int(n.StartPoint().Row) + 1
				end := int(n.EndPoint().Row) + 1
				addDef(nsSym, graph.KindPackage, start, end, nil)
			}
			for _, ri := range clojureRequireImports(n, src, e.lang) {
				result.Edges = append(result.Edges, &graph.Edge{
					From: fileNode.ID, To: "unresolved::import::" + ri.name,
					Kind: graph.EdgeImports, FilePath: filePath, Line: ri.line,
				})
			}
		case "defn", "defn-":
			name := clojureNthSym(n, 1, src, e.lang)
			start := int(n.StartPoint().Row) + 1
			end := int(n.EndPoint().Row) + 1
			addDef(name, graph.KindFunction, start, end, nil)
		case "defmacro":
			name := clojureNthSym(n, 1, src, e.lang)
			start := int(n.StartPoint().Row) + 1
			end := int(n.EndPoint().Row) + 1
			addDef(name, graph.KindFunction, start, end, map[string]any{"macro": true})
		case "defrecord", "deftype":
			name := clojureNthSym(n, 1, src, e.lang)
			start := int(n.StartPoint().Row) + 1
			end := int(n.EndPoint().Row) + 1
			addDef(name, graph.KindType, start, end, nil)
		case "defprotocol":
			name := clojureNthSym(n, 1, src, e.lang)
			start := int(n.StartPoint().Row) + 1
			end := int(n.EndPoint().Row) + 1
			addDef(name, graph.KindInterface, start, end, nil)
		case "def":
			name := clojureNthSym(n, 1, src, e.lang)
			start := int(n.StartPoint().Row) + 1
			end := int(n.EndPoint().Row) + 1
			addDef(name, graph.KindVariable, start, end, nil)
		}
	})
	_ = nsName

	// Call sites: any list_lit whose head sym_lit is not a special form
	// or a known def-form. Attribute to the enclosing def-function.
	funcRanges := buildFuncRanges(result)
	walkNodes(root, func(n *sitter.Node) {
		if n == nil || parser.NodeType(n, e.lang) != "list_lit" {
			return
		}
		head := clojureListHead(n, src, e.lang)
		if head == "" || isClojureSpecialForm(head) {
			return
		}
		line := int(n.StartPoint().Row) + 1
		callerID := findEnclosingFunc(funcRanges, line)
		if callerID == "" {
			return
		}
		target := filePath + "::" + head
		if callerID == target {
			return
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: callerID, To: "unresolved::" + head,
			Kind: graph.EdgeCalls, FilePath: filePath, Line: line,
		})
	})

	return result, nil
}

// clojureListHead returns the text of the first named `sym_lit` child of
// a list_lit (its "head" symbol).
func clojureListHead(list *sitter.Node, src []byte, lang *sitter.Language) string {
	for i := 0; i < int(list.NamedChildCount()); i++ {
		c := list.NamedChild(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, lang) == "sym_lit" {
			return symLitName(c, src, lang)
		}
	}
	return ""
}

// clojureNthSym returns the n-th (0-based) named sym_lit child's name.
func clojureNthSym(list *sitter.Node, n int, src []byte, lang *sitter.Language) string {
	count := 0
	for i := 0; i < int(list.NamedChildCount()); i++ {
		c := list.NamedChild(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, lang) != "sym_lit" {
			continue
		}
		if count == n {
			return symLitName(c, src, lang)
		}
		count++
	}
	return ""
}

// symLitName pulls the raw name from a sym_lit node via its sym_name
// child (falls back to the node's own text).
func symLitName(sym *sitter.Node, src []byte, lang *sitter.Language) string {
	for i := 0; i < int(sym.NamedChildCount()); i++ {
		c := sym.NamedChild(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, lang) == "sym_name" {
			return c.Text(src)
		}
	}
	return sym.Text(src)
}

type clojureImportHit struct {
	name string
	line int
}

// clojureRequireImports pulls module names out of (:require …) and
// (:import …) forms inside an `ns` list. Each require-vec's first
// symbol (or the bare sym_lit under :import) becomes an import edge.
func clojureRequireImports(nsList *sitter.Node, src []byte, lang *sitter.Language) []clojureImportHit {
	var out []clojureImportHit
	for i := 0; i < int(nsList.NamedChildCount()); i++ {
		clause := nsList.NamedChild(i)
		if clause == nil || parser.NodeType(clause, lang) != "list_lit" {
			continue
		}
		// First child of the clause should be a :kwd (kwd_lit with
		// sym_name/ kwd_name text "require" or "import").
		kwd := ""
		for j := 0; j < int(clause.NamedChildCount()); j++ {
			c := clause.NamedChild(j)
			if c == nil {
				continue
			}
			if parser.NodeType(c, lang) == "kwd_lit" {
				for k := 0; k < int(c.NamedChildCount()); k++ {
					sub := c.NamedChild(k)
					if sub != nil && parser.NodeType(sub, lang) == "kwd_name" {
						kwd = sub.Text(src)
						break
					}
				}
				break
			}
		}
		if kwd != "require" && kwd != "import" {
			continue
		}
		// Subsequent vec_lit children carry the module refs.
		for j := 0; j < int(clause.NamedChildCount()); j++ {
			c := clause.NamedChild(j)
			if c == nil {
				continue
			}
			switch parser.NodeType(c, lang) {
			case "vec_lit":
				// First sym_lit inside.
				if name := firstSymName(c, src, lang); name != "" {
					out = append(out, clojureImportHit{name: name, line: int(c.StartPoint().Row) + 1})
				}
			case "sym_lit":
				// Bare import like (:import java.util.Date).
				if name := symLitName(c, src, lang); name != "" {
					out = append(out, clojureImportHit{name: name, line: int(c.StartPoint().Row) + 1})
				}
			}
		}
	}
	return out
}

func firstSymName(node *sitter.Node, src []byte, lang *sitter.Language) string {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		c := node.NamedChild(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, lang) == "sym_lit" {
			return symLitName(c, src, lang)
		}
	}
	return ""
}

func isClojureSpecialForm(s string) bool {
	switch s {
	case "if", "do", "let", "fn", "def", "defn", "defn-", "defmacro",
		"defrecord", "deftype", "defprotocol", "ns", "require", "use",
		"import", "quote", "loop", "recur", "throw", "try", "catch",
		"finally", "cond", "case", "when", "when-not", "when-let",
		"if-let", "for", "doseq", "dotimes", "str", "+", "-", "*", "/",
		":require", ":import":
		return true
	}
	return false
}

var _ parser.Extractor = (*ClojureExtractor)(nil)
