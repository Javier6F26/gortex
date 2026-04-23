package languages

import (
	"regexp"
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// asmCallLineRe is a narrow fallback for call-style mnemonics when the
// grammar drops into ERROR recovery on dialect-specific addressing
// modes (e.g. GAS `leaq msg(%rip), %rdi`). We scan every source line
// for a leading call/jsr/bl/… mnemonic followed by a bare target ident.
var asmCallLineRe = regexp.MustCompile(`^\s*(call|calll|callq|callw|jsr|bl|blx|bsr|jal|jalr|jmp)\s+([A-Za-z_.][\w.$]*)`)

// AssemblyExtractor covers multiple dialects from one extractor: NASM,
// MASM, GAS / AT&T, WLA-DX, CA65, and ARM UAL. The set of concepts
// each dialect expresses is small enough to share: labels are the
// unit of code, `call` / `jsr` / `bl` / `jmp` / `bsr` are inter-label
// edges, `.global` / `.globl` / `global` declare exports, `.extern` /
// `extern` / `.import` declare imports, and NASM `%include` plus GAS
// `.include` plus WLA-DX `.INCLUDE` carry file dependencies.
//
// The odvcencio grammar uses a tiny shape for asm:
//
//   - label       (name:) → child `ident`
//   - meta        (`.foo ...`) → child `meta_ident` + args as `ident`/`string`
//   - instruction (`mnemonic operand, …`) → child `word` + operand `ident`s
//
// The `%include` NASM prefix parses as ERROR (grammar is GAS-leaning);
// and in NASM the bare `global`/`extern` keywords parse as
// `instruction` rather than `meta`. We handle both: walk the AST for
// labels/meta/instruction, and for the few tokens the grammar misses
// (specifically NASM `%include "file"`) we fall through to the raw
// source scan.
//
// Each label is modelled as a function node so the rest of the
// graph-query surface (get_callers, find_usages, etc.) works without
// a dedicated asm-aware query path.
type AssemblyExtractor struct {
	lang *sitter.Language
}

func NewAssemblyExtractor() *AssemblyExtractor {
	return &AssemblyExtractor{lang: grammars.AsmLanguage()}
}

func (e *AssemblyExtractor) Language() string { return "assembly" }
func (e *AssemblyExtractor) Extensions() []string {
	return []string{".asm", ".s", ".S", ".nasm", ".masm", ".inc", ".a65"}
}

// asmCallMnemonics is the set of call/jump mnemonics across dialects
// that should produce EdgeCalls edges.
var asmCallMnemonics = map[string]bool{
	"call": true, "calll": true, "callq": true, "callw": true,
	"jsr": true, "bl": true, "blx": true, "bsr": true,
	"jal": true, "jalr": true, "jmp": true,
}

// asmGlobalDirectives is the set of directive names that declare a
// symbol as globally exported.
var asmGlobalDirectives = map[string]bool{
	"global": true, "globl": true, "public": true,
	".global": true, ".globl": true, ".public": true,
}

// asmExternDirectives is the set of directive names that import an
// external symbol.
var asmExternDirectives = map[string]bool{
	"extern": true, "externdef": true, "import": true,
	".extern": true, ".externdef": true, ".import": true,
}

// asmIncludeDirectives is the set of directive names that include a
// file.
var asmIncludeDirectives = map[string]bool{
	".include": true, ".INCLUDE": true,
}

func (e *AssemblyExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	lines := strings.Split(string(src), "\n")
	result := &parser.ExtractionResult{}

	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: len(lines),
		Language: "assembly",
	}
	result.Nodes = append(result.Nodes, fileNode)

	if len(src) == 0 {
		return result, nil
	}

	tree, err := parser.ParseFile(src, e.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()
	root := tree.RootNode()

	// Phase 1: collect labels (functions) + global/extern/include directives
	// + call-like instructions. One walk, dispatch by node type.
	type labelHit struct {
		name string
		line int
	}
	var labels []labelHit
	seen := make(map[string]bool)

	type callHit struct {
		target string
		line   int
	}
	var calls []callHit

	type globalHit struct {
		name string
	}
	var globals []globalHit

	type importHit struct {
		name string
		line int
	}
	var externs []importHit
	var includes []importHit

	walkNodes(root, func(n *sitter.Node) {
		if n == nil {
			return
		}
		nt := parser.NodeType(n, e.lang)
		start := int(n.StartPoint().Row) + 1

		switch nt {
		case "label":
			name := asmLabelName(n, src, e.lang)
			if name == "" || isAsmDirective(strings.ToLower(name)) {
				return
			}
			labels = append(labels, labelHit{name: name, line: start})

		case "meta":
			// meta_ident child is the directive name; remaining child(ren)
			// are args (ident/string).
			head, args := asmMetaParts(n, src, e.lang)
			lower := strings.ToLower(head)
			switch {
			case asmGlobalDirectives[lower]:
				for _, a := range args {
					globals = append(globals, globalHit{name: a})
				}
			case asmExternDirectives[lower]:
				for _, a := range args {
					externs = append(externs, importHit{name: a, line: start})
				}
			case asmIncludeDirectives[head] || asmIncludeDirectives[lower]:
				for _, a := range args {
					includes = append(includes, importHit{name: a, line: start})
				}
			}

		case "instruction":
			// word is first named child; operand is an `ident` or similar.
			word, operands := asmInstructionParts(n, src, e.lang)
			lower := strings.ToLower(word)
			switch {
			case asmCallMnemonics[lower]:
				if len(operands) > 0 {
					calls = append(calls, callHit{target: operands[0], line: start})
				}
			case asmGlobalDirectives[lower]:
				// NASM: `global foo` parses as instruction.
				for _, a := range operands {
					globals = append(globals, globalHit{name: a})
				}
			case asmExternDirectives[lower]:
				// NASM: `extern foo` parses as instruction.
				for _, a := range operands {
					externs = append(externs, importHit{name: a, line: start})
				}
			}
		}
	})

	// NASM `%include "file"` parses as ERROR; recover via a narrow line
	// scan. Also recovers MASM `%include` variants.
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "%include") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "%include"))
		rest = strings.Trim(rest, "\"'<>")
		if rest == "" {
			continue
		}
		includes = append(includes, importHit{name: rest, line: i + 1})
	}

	// Secondary line-scan for call-style mnemonics. The grammar drops
	// into ERROR recovery on GAS RIP-relative addressing (`leaq
	// msg(%rip), …`), ARM bracket operands, and a few other dialect
	// idioms — and everything after the error is lost. A regex sweep
	// backstops those misses without double-counting, thanks to the
	// (line, target) dedup set below.
	already := make(map[[2]int]bool)
	for _, ch := range calls {
		already[[2]int{ch.line, hash32(ch.target)}] = true
	}
	for i, line := range lines {
		m := asmCallLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		target := m[2]
		k := [2]int{i + 1, hash32(target)}
		if already[k] {
			continue
		}
		already[k] = true
		calls = append(calls, callHit{target: target, line: i + 1})
	}

	// Emit label function nodes with proximity-based end lines.
	for i, lh := range labels {
		endLine := len(lines)
		if i+1 < len(labels) {
			endLine = labels[i+1].line - 1
		}
		id := filePath + "::" + lh.name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindFunction, Name: lh.name,
			FilePath: filePath, StartLine: lh.line, EndLine: endLine,
			Language: "assembly",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: lh.line,
		})
	}

	// Apply `global` meta flags onto matching label nodes.
	for _, gh := range globals {
		id := filePath + "::" + gh.name
		if n := findNode(result.Nodes, id); n != nil {
			if n.Meta == nil {
				n.Meta = map[string]any{}
			}
			n.Meta["global"] = true
		}
	}

	// Emit extern → import edges.
	for _, eh := range externs {
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::" + eh.name,
			Kind: graph.EdgeImports, FilePath: filePath, Line: eh.line,
		})
	}

	// Emit include → import edges (file dependencies).
	for _, ih := range includes {
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + ih.name,
			Kind: graph.EdgeImports, FilePath: filePath, Line: ih.line,
		})
	}

	// Emit call edges, attributing them to the enclosing label by line.
	funcRanges := buildFuncRanges(result)
	for _, ch := range calls {
		callerID := findEnclosingFunc(funcRanges, ch.line)
		if callerID == "" || strings.HasSuffix(callerID, "::"+ch.target) {
			continue
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: callerID, To: "unresolved::" + ch.target,
			Kind: graph.EdgeCalls, FilePath: filePath, Line: ch.line,
		})
	}

	return result, nil
}

// asmLabelName returns the name of a `label` node (first ident child).
func asmLabelName(n *sitter.Node, src []byte, lang *sitter.Language) string {
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		if parser.NodeType(c, lang) == "ident" {
			return strings.TrimSpace(c.Text(src))
		}
	}
	return ""
}

// asmMetaParts returns (directive, args) from a `meta` node. The first
// named child is `meta_ident` (directive keyword including any leading
// dot); remaining named children are ident/string arguments.
func asmMetaParts(n *sitter.Node, src []byte, lang *sitter.Language) (string, []string) {
	var head string
	var args []string
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch parser.NodeType(c, lang) {
		case "meta_ident":
			if head == "" {
				head = strings.TrimSpace(c.Text(src))
			}
		case "ident":
			args = append(args, asmIdentText(c, src, lang))
		case "string":
			raw := strings.TrimSpace(c.Text(src))
			raw = strings.Trim(raw, "\"'<>")
			if raw != "" {
				args = append(args, raw)
			}
		}
	}
	return head, args
}

// asmInstructionParts returns (word, operands) from an `instruction`
// node. Word is the first `word` child; operands are subsequent
// `ident`/`string` children.
func asmInstructionParts(n *sitter.Node, src []byte, lang *sitter.Language) (string, []string) {
	var word string
	var operands []string
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch parser.NodeType(c, lang) {
		case "word":
			if word == "" {
				word = strings.TrimSpace(c.Text(src))
			}
		case "ident":
			operands = append(operands, asmIdentText(c, src, lang))
		case "string":
			raw := strings.TrimSpace(c.Text(src))
			raw = strings.Trim(raw, "\"'<>")
			if raw != "" {
				operands = append(operands, raw)
			}
		}
	}
	return word, operands
}

// asmIdentText extracts a clean identifier from an `ident` node. The
// grammar wraps idents in `reg > word` layers; we prefer the deepest
// `word` text which strips `%` prefixes etc. Falls back to the raw
// node text.
func asmIdentText(n *sitter.Node, src []byte, lang *sitter.Language) string {
	// Try to find a `word` descendant.
	var word string
	walkNodes(n, func(c *sitter.Node) {
		if word != "" || c == nil {
			return
		}
		if parser.NodeType(c, lang) == "word" {
			word = strings.TrimSpace(c.Text(src))
		}
	})
	if word != "" {
		return word
	}
	return strings.TrimSpace(n.Text(src))
}

func findNode(nodes []*graph.Node, id string) *graph.Node {
	for _, n := range nodes {
		if n.ID == id {
			return n
		}
	}
	return nil
}

// isAsmDirective identifies tokens that look like labels but are
// actually segment / section directives masquerading as `name:`
// syntax in some toolchains. False positives here are cheap; the
// directive gets filtered, the label miss is recovered on the next
// pass through the graph.
func isAsmDirective(s string) bool {
	switch s {
	case ".text", ".data", ".bss", ".rodata", ".section", ".global",
		".globl", ".extern", ".include", ".equ", ".org", ".byte",
		".word", ".long", ".quad", ".ascii", ".asciz", ".string",
		"section", "segment", "ends", "end", "proc", "endp":
		return true
	}
	return false
}

// hash32 gives a cheap stable integer key for target strings used in
// the per-line dedup set inside Extract.
func hash32(s string) int {
	h := 2166136261
	for i := 0; i < len(s); i++ {
		h ^= int(s[i])
		h *= 16777619
	}
	return h
}

var _ parser.Extractor = (*AssemblyExtractor)(nil)
