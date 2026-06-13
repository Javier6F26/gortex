// Package cfg builds intra-procedural control-flow graphs from a
// single function's source text, on demand, and runs a classic
// GEN/KILL reaching-definitions fixpoint over them.
//
// The package is deliberately query-time-only: nothing here runs at
// index time and nothing touches the whole graph. A caller hands in
// one function's source (typically sliced out of a file by the
// symbol's line range), names the language, and gets back:
//
//   - basic blocks, each holding the ordered statements it executes
//     with their line spans and per-statement def/use variable sets;
//   - labeled edges between blocks (seq / true / false / loop_back /
//     break / continue / return / case / exception / finally);
//   - via ReachingDefinitions, statement-granular def→use chains:
//     for every variable read, the set of definitions that can reach
//     it along some path.
//
// Seven languages are covered by per-language control-construct
// tables: Go, Python, JavaScript, TypeScript, Java, Rust, and Ruby.
// Parsing reuses the parser package's pooled tree-sitter parsers
// (errored parsers are closed, never pooled — see parser.ParseFile).
//
// Scope model. Definitions and uses are tracked by variable NAME
// within the function: parameters are entry-block definitions,
// assignments / declarations / augmented assigns define, identifier
// reads use. Nested function literals (closures, lambdas, inner
// defs) are treated as opaque — their bodies execute at an unknown
// later time, so neither their assignments nor their reads are
// attributed to the enclosing function's statements. Reads of names
// never defined in the function (globals, package symbols) simply
// produce no def→use chain.
package cfg

import (
	"errors"
	"fmt"
	"strings"

	"github.com/zzet/gortex/internal/parser"
	sitter "github.com/zzet/gortex/internal/parser/tsitter"
)

// EdgeLabel classifies a control-flow edge between two basic blocks.
type EdgeLabel string

const (
	LabelSeq       EdgeLabel = "seq"
	LabelTrue      EdgeLabel = "true"
	LabelFalse     EdgeLabel = "false"
	LabelLoopBack  EdgeLabel = "loop_back"
	LabelBreak     EdgeLabel = "break"
	LabelContinue  EdgeLabel = "continue"
	LabelReturn    EdgeLabel = "return"
	LabelCase      EdgeLabel = "case"
	LabelException EdgeLabel = "exception"
	LabelFinally   EdgeLabel = "finally"
)

// Statement is one executable statement (or synthetic pseudo-
// statement: parameter binding, branch condition, case label) inside
// a basic block. Lines are 1-based and already shifted by
// Options.LineOffset so they are file-absolute when the caller
// passes the function's position.
type Statement struct {
	Index     int      `json:"index"`
	Block     int      `json:"block"`
	StartLine int      `json:"start_line"`
	EndLine   int      `json:"end_line"`
	Text      string   `json:"text"`
	Kind      string   `json:"kind,omitempty"`
	Defs      []string `json:"defs,omitempty"`
	Uses      []string `json:"uses,omitempty"`
}

// Block is a basic block: a maximal straight-line statement sequence
// with control transfers only at the end.
type Block struct {
	ID    int
	Label string
	Stmts []*Statement
}

// Edge is one labeled control-flow edge.
type Edge struct {
	From  int       `json:"from"`
	To    int       `json:"to"`
	Label EdgeLabel `json:"label"`
}

// CFG is a per-function control-flow graph. Blocks[Entry] holds the
// synthetic parameter definitions; Blocks[Exit] is the empty sink
// every return/fall-off-the-end edge targets.
type CFG struct {
	FuncName string
	Language string
	Entry    int
	Exit     int
	Blocks   []*Block
	Edges    []Edge
	Stmts    []*Statement
}

// Options tunes Build.
type Options struct {
	// LineOffset is added to every (1-based) snippet line so the CFG
	// reports file-absolute lines. Pass the function node's
	// StartLine-1 when src is sliced out of a larger file.
	LineOffset int
	// FuncName overrides the name discovered from the AST.
	FuncName string
}

// Build parses src as one function in the given language and
// constructs its control-flow graph. src must contain (or start
// with) a single function/method definition — the first function-
// like node found in the parse tree is used. Languages whose
// methods don't parse standalone (Java, JS/TS class methods) are
// retried inside a synthetic class wrapper.
func Build(src []byte, language string, opts Options) (*CFG, error) {
	spec := specFor(language)
	if spec == nil {
		return nil, fmt.Errorf("cfg: unsupported language %q", language)
	}
	prepared := src
	if spec.dedent {
		prepared = dedent(src)
	}

	tree, err := parser.ParseFile(prepared, spec.grammar())
	if err != nil {
		return nil, fmt.Errorf("cfg: parse: %w", err)
	}
	fn := findFuncRoot(tree.RootNode(), spec)
	wrapOffset := 0
	if fn == nil && spec.classWrap {
		tree.Close()
		wrapped := append([]byte("class __gortexcfg__ {\n"), prepared...)
		wrapped = append(wrapped, []byte("\n}")...)
		tree, err = parser.ParseFile(wrapped, spec.grammar())
		if err != nil {
			return nil, fmt.Errorf("cfg: parse (wrapped): %w", err)
		}
		fn = findFuncRoot(tree.RootNode(), spec)
		wrapOffset = -1
		prepared = wrapped
	}
	defer tree.Close()
	if fn == nil {
		return nil, errors.New("cfg: no function definition found in source")
	}

	c := &CFG{Language: spec.name, FuncName: opts.FuncName}
	if c.FuncName == "" {
		if nameNode := fn.ChildByFieldName("name"); nameNode != nil {
			c.FuncName = nameNode.Content(prepared)
		}
	}

	b := &builder{
		spec:       spec,
		src:        prepared,
		lineOffset: opts.LineOffset + wrapOffset,
		cfg:        c,
		edgeSeen:   map[edgeKey]bool{},
	}
	entry := b.newBlock("entry")
	exit := b.newBlock("exit")
	c.Entry, c.Exit = entry.ID, exit.ID

	// Parameters become block-0 definitions: one synthetic statement
	// per parameter so every chain points at its own binding site.
	b.cur = entry
	for _, p := range spec.params(fn, prepared) {
		st := b.record(p.line, p.line, p.name, "param")
		st.Defs = []string{p.name}
	}

	body := spec.bodyOf(fn)
	if body == nil {
		// A bodiless declaration (interface method, abstract method)
		// still yields a degenerate-but-valid CFG.
		b.edge(entry, exit, LabelSeq)
		return c, nil
	}

	first := b.newBlock("body")
	b.edge(entry, first, LabelSeq)
	b.cur = first
	b.buildStmt(body)
	if b.cur != nil {
		b.edge(b.cur, exit, LabelSeq)
	}
	return c, nil
}

// findFuncRoot returns the first function-like node in breadth-first
// order, so the outermost definition wins when functions nest.
func findFuncRoot(root *sitter.Node, spec *langSpec) *sitter.Node {
	if root == nil {
		return nil
	}
	queue := []*sitter.Node{root}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if spec.funcKinds[n.Type()] {
			return n
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			if c := n.NamedChild(i); c != nil {
				queue = append(queue, c)
			}
		}
	}
	return nil
}

// dedent strips the longest common leading whitespace prefix from
// every non-blank line. Needed for indentation-sensitive snippets
// (a Python method sliced out of a class body would otherwise parse
// as an indentation error).
func dedent(src []byte) []byte {
	lines := strings.Split(string(src), "\n")
	prefix := ""
	first := true
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		indent := ln[:len(ln)-len(strings.TrimLeft(ln, " \t"))]
		if first {
			prefix = indent
			first = false
			continue
		}
		for !strings.HasPrefix(ln, prefix) {
			prefix = prefix[:len(prefix)-1]
		}
		if prefix == "" {
			return src
		}
	}
	if prefix == "" {
		return src
	}
	for i, ln := range lines {
		lines[i] = strings.TrimPrefix(ln, prefix)
	}
	return []byte(strings.Join(lines, "\n"))
}

// StatementAt returns the statement covering the given (file-
// absolute) line that defines name, or nil. Used by the dataflow
// refinement layer to anchor graph binding nodes onto CFG
// statements.
func (c *CFG) StatementAt(line int, definedVar string) *Statement {
	var best *Statement
	for _, st := range c.Stmts {
		if line < st.StartLine || line > st.EndLine {
			continue
		}
		found := false
		for _, d := range st.Defs {
			if d == definedVar {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		// Prefer the tightest span when statements nest (loop
		// headers cover their condition line, etc.).
		if best == nil || (st.EndLine-st.StartLine) < (best.EndLine-best.StartLine) {
			best = st
		}
	}
	return best
}
