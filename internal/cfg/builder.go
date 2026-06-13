package cfg

import (
	"strings"

	sitter "github.com/zzet/gortex/internal/parser/tsitter"
)

// maxStmtText caps the recorded statement text so giant one-liners
// don't bloat tool responses.
const maxStmtText = 120

// frame is one entry of the break/continue resolution stack. Loops
// push a frame with both targets; switch statements in languages
// where `break` exits the switch push a frame with only breakTo.
type frame struct {
	label      string
	continueTo *Block
	breakTo    *Block
	isLoop     bool
}

// builder drives CFG construction. cur is the block receiving the
// next statement; nil means the current position is past a
// terminator (return/break/…) — the next statement starts a fresh,
// unreachable block so its defs/uses still surface.
type builder struct {
	spec       *langSpec
	src        []byte
	lineOffset int
	cfg        *CFG

	cur                *Block
	frames             []frame
	pendingLabel       string
	pendingFallthrough bool

	edgeSeen map[edgeKey]bool
}

type edgeKey struct {
	from, to int
	label    EdgeLabel
}

func (b *builder) newBlock(label string) *Block {
	bl := &Block{ID: len(b.cfg.Blocks), Label: label}
	b.cfg.Blocks = append(b.cfg.Blocks, bl)
	return bl
}

func (b *builder) edge(from, to *Block, label EdgeLabel) {
	if from == nil || to == nil {
		return
	}
	k := edgeKey{from.ID, to.ID, label}
	if b.edgeSeen[k] {
		return
	}
	b.edgeSeen[k] = true
	b.cfg.Edges = append(b.cfg.Edges, Edge{From: from.ID, To: to.ID, Label: label})
}

// moveTo links cur to bl sequentially and makes bl current.
func (b *builder) moveTo(bl *Block) {
	if b.cur != nil {
		b.edge(b.cur, bl, LabelSeq)
	}
	b.cur = bl
}

// ensureCur guarantees a current block, opening an unreachable one
// when the previous statement terminated control flow.
func (b *builder) ensureCur() {
	if b.cur == nil {
		b.cur = b.newBlock("unreachable")
	}
}

func (b *builder) pushFrame(f frame) { b.frames = append(b.frames, f) }
func (b *builder) popFrame()         { b.frames = b.frames[:len(b.frames)-1] }

// takeLabel consumes the label set by an enclosing labeled
// statement, if any.
func (b *builder) takeLabel() string {
	l := b.pendingLabel
	b.pendingLabel = ""
	return l
}

// record appends a synthetic statement with explicit position/text.
func (b *builder) record(startLine, endLine int, text, kind string) *Statement {
	st := &Statement{
		Index:     len(b.cfg.Stmts),
		Block:     b.cur.ID,
		StartLine: startLine + b.lineOffset,
		EndLine:   endLine + b.lineOffset,
		Text:      text,
		Kind:      kind,
	}
	b.cfg.Stmts = append(b.cfg.Stmts, st)
	b.cur.Stmts = append(b.cur.Stmts, st)
	return st
}

// recordNode appends a statement positioned at n without running
// def/use extraction (callers fill Defs/Uses themselves).
func (b *builder) recordNode(n *sitter.Node, kind string) *Statement {
	return b.record(int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, stmtText(n, b.src), kind)
}

// addStmt appends a statement for n with def/use extraction.
func (b *builder) addStmt(n *sitter.Node, kind string) *Statement {
	if n == nil {
		return nil
	}
	st := b.recordNode(n, kind)
	st.Defs, st.Uses = extractDefUse(b.spec, b.src, n, false)
	return st
}

// leaf records n as a plain statement in the current block.
func (b *builder) leaf(n *sitter.Node, kind string) {
	b.ensureCur()
	b.addStmt(n, kind)
}

// stmtText renders the statement's first source line, trimmed and
// capped; multi-line statements get an ellipsis.
func stmtText(n *sitter.Node, src []byte) string {
	text := n.Content(src)
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i] + " …"
	}
	text = strings.TrimSpace(text)
	if len(text) > maxStmtText {
		text = text[:maxStmtText] + "…"
	}
	return text
}

// buildStmt processes one statement node: control constructs are
// consumed by the language dispatch table, everything else is a leaf.
func (b *builder) buildStmt(n *sitter.Node) {
	if n == nil {
		return
	}
	if n.Type() == "comment" {
		return
	}
	if b.spec.dispatch(b, n) {
		return
	}
	b.leaf(n, "")
}

// buildSeq processes every named child of n as a statement.
func (b *builder) buildSeq(n *sitter.Node) {
	for i := 0; i < int(n.NamedChildCount()); i++ {
		if c := n.NamedChild(i); c != nil {
			b.buildStmt(c)
		}
	}
}

// ---------------------------------------------------------------------------
// if / else
// ---------------------------------------------------------------------------

// buildIf wires the classic diamond. alt may itself be an if (else-
// if chains) — the recursive buildStmt handles it through dispatch.
func (b *builder) buildIf(init, cond, then, alt *sitter.Node) {
	b.ensureCur()
	if init != nil {
		b.buildStmt(init)
	}
	if cond != nil {
		b.addStmt(cond, "cond")
	}
	head := b.cur
	after := b.newBlock("if_end")

	thenBlock := b.newBlock("then")
	b.edge(head, thenBlock, LabelTrue)
	b.cur = thenBlock
	if then != nil {
		b.buildStmt(then)
	}
	if b.cur != nil {
		b.edge(b.cur, after, LabelSeq)
	}

	if alt != nil {
		elseBlock := b.newBlock("else")
		b.edge(head, elseBlock, LabelFalse)
		b.cur = elseBlock
		b.buildStmt(alt)
		if b.cur != nil {
			b.edge(b.cur, after, LabelSeq)
		}
	} else {
		b.edge(head, after, LabelFalse)
	}
	b.cur = after
}

// buildIfChain handles grammars that stack elif/else clauses as
// sibling `alternative` fields (Python) instead of nesting them.
func (b *builder) buildIfChain(cond, cons *sitter.Node, alts []*sitter.Node) {
	b.ensureCur()
	if cond != nil {
		b.addStmt(cond, "cond")
	}
	head := b.cur
	after := b.newBlock("if_end")

	thenBlock := b.newBlock("then")
	b.edge(head, thenBlock, LabelTrue)
	b.cur = thenBlock
	if cons != nil {
		b.buildStmt(cons)
	}
	if b.cur != nil {
		b.edge(b.cur, after, LabelSeq)
	}

	if len(alts) == 0 {
		b.edge(head, after, LabelFalse)
		b.cur = after
		return
	}

	elseBlock := b.newBlock("else")
	b.edge(head, elseBlock, LabelFalse)
	b.cur = elseBlock
	first := alts[0]
	if first.Type() == "elif_clause" {
		b.buildIfChain(first.ChildByFieldName("condition"), first.ChildByFieldName("consequence"), alts[1:])
	} else {
		// else_clause terminates the chain.
		b.buildStmt(first)
	}
	if b.cur != nil {
		b.edge(b.cur, after, LabelSeq)
	}
	b.cur = after
}

// ---------------------------------------------------------------------------
// loops
// ---------------------------------------------------------------------------

// loopParts feeds buildLoop. Exactly one of {cond, headerStmt,
// infinite} shapes the header:
//   - cond: pre/post-test condition loop (while / for / do-while)
//   - headerStmt: for-in/range loop; the header statement defines the
//     loop variables and reads the iterable. When
//     headerStmtOnlyHeaderFields is set the node also contains the
//     body, so def/use extraction is restricted to the header fields.
//   - infinite: no condition (Go `for {}`, Rust `loop {}`).
type loopParts struct {
	init, cond, update, body   *sitter.Node
	headerStmt                 *sitter.Node
	headerStmtOnlyHeaderFields bool
	postTest                   bool
	infinite                   bool
	// elseNode is a Python for/while-else clause. It runs only on a
	// normal (non-break) loop exit, so the builder routes the header's
	// False edge through the else block while `break` jumps past it to
	// a dedicated join block. Nil for every other language.
	elseNode *sitter.Node
}

func (b *builder) buildLoop(p loopParts) {
	b.ensureCur()
	if p.init != nil {
		b.buildStmt(p.init)
	}
	label := b.takeLabel()

	if p.postTest {
		bodyBlock := b.newBlock("loop_body")
		b.moveTo(bodyBlock)
		header := b.newBlock("loop_header")
		after := b.newBlock("loop_end")
		b.pushFrame(frame{label: label, continueTo: header, breakTo: after, isLoop: true})
		if p.body != nil {
			b.buildStmt(p.body)
		}
		b.popFrame()
		if b.cur != nil {
			b.edge(b.cur, header, LabelSeq)
		}
		b.cur = header
		if p.cond != nil {
			b.addStmt(p.cond, "cond")
		}
		b.edge(header, bodyBlock, LabelLoopBack)
		b.edge(header, after, LabelFalse)
		b.cur = after
		return
	}

	header := b.newBlock("loop_header")
	b.moveTo(header)
	if p.headerStmt != nil {
		b.addLoopHeaderStmt(p)
	} else if p.cond != nil {
		b.addStmt(p.cond, "cond")
	}
	after := b.newBlock("loop_end")
	bodyBlock := b.newBlock("loop_body")
	// With a for/while-else clause, the normal exit (header False) runs
	// the else before reaching the join, but `break` must skip it.
	// Route break edges at a separate join block; without an else the
	// join is the loop_end itself so behaviour is unchanged.
	breakTarget := after
	if p.elseNode != nil {
		breakTarget = b.newBlock("loop_join")
	}
	if p.infinite {
		b.edge(header, bodyBlock, LabelSeq)
	} else {
		b.edge(header, bodyBlock, LabelTrue)
		b.edge(header, after, LabelFalse)
	}

	var updateBlock *Block
	contTarget := header
	if p.update != nil {
		updateBlock = b.newBlock("loop_update")
		contTarget = updateBlock
	}
	b.pushFrame(frame{label: label, continueTo: contTarget, breakTo: breakTarget, isLoop: true})
	b.cur = bodyBlock
	if p.body != nil {
		b.buildStmt(p.body)
	}
	b.popFrame()
	if updateBlock != nil {
		if b.cur != nil {
			b.edge(b.cur, updateBlock, LabelSeq)
		}
		b.cur = updateBlock
		b.buildStmt(p.update)
		if b.cur != nil {
			b.edge(b.cur, header, LabelLoopBack)
		}
	} else if b.cur != nil {
		b.edge(b.cur, header, LabelLoopBack)
	}
	b.cur = after
	if p.elseNode != nil {
		// The else clause runs on the False (no-break) exit; break
		// edges already bypass it by targeting the join directly.
		b.buildStmt(p.elseNode)
		if b.cur != nil {
			b.edge(b.cur, breakTarget, LabelSeq)
		}
		b.cur = breakTarget
	}
}

// addLoopHeaderStmt records the for-in header: loop variables are
// definitions, the iterable is a use. When the header node embeds
// the body (Python for, JS for-in, Java enhanced-for, Rust for, Ruby
// for) only the header fields are inspected.
func (b *builder) addLoopHeaderStmt(p loopParts) {
	n := p.headerStmt
	if !p.headerStmtOnlyHeaderFields {
		b.addStmt(n, "loop")
		return
	}
	defsNode, usesNode := forInHeaderFields(n)
	startLine := int(n.StartPoint().Row) + 1
	endLine := startLine
	if usesNode != nil {
		endLine = int(usesNode.EndPoint().Row) + 1
	}
	text := stmtText(n, b.src)
	st := b.record(startLine, endLine, text, "loop")
	if defsNode != nil {
		defs, _ := extractDefUse(b.spec, b.src, defsNode, true)
		st.Defs = defs
	}
	if usesNode != nil {
		_, uses := extractDefUse(b.spec, b.src, usesNode, false)
		st.Uses = uses
	}
}

// forInHeaderFields probes the field-name pairs the supported
// grammars use for "<vars> in <iterable>" headers.
func forInHeaderFields(n *sitter.Node) (defs, uses *sitter.Node) {
	if l := n.ChildByFieldName("left"); l != nil {
		return l, n.ChildByFieldName("right")
	}
	if p := n.ChildByFieldName("pattern"); p != nil {
		return p, n.ChildByFieldName("value")
	}
	if name := n.ChildByFieldName("name"); name != nil {
		return name, n.ChildByFieldName("value")
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// jumps
// ---------------------------------------------------------------------------

// findFrame resolves a break/continue target. continue skips frames
// without a continue target (switches); a label restricts the match.
func (b *builder) findFrame(label string, needContinue bool) *frame {
	for i := len(b.frames) - 1; i >= 0; i-- {
		f := &b.frames[i]
		if needContinue && f.continueTo == nil {
			continue
		}
		if label != "" && f.label != label {
			continue
		}
		return f
	}
	return nil
}

func (b *builder) buildBreak(n *sitter.Node, label string) {
	b.ensureCur()
	b.addStmt(n, "break")
	if f := b.findFrame(label, false); f != nil {
		b.edge(b.cur, f.breakTo, LabelBreak)
	} else {
		// break outside any loop/switch — treat as function exit so
		// the flow graph stays connected.
		b.edge(b.cur, b.cfg.Blocks[b.cfg.Exit], LabelBreak)
	}
	b.cur = nil
}

func (b *builder) buildContinue(n *sitter.Node, label string) {
	b.ensureCur()
	b.addStmt(n, "continue")
	if f := b.findFrame(label, true); f != nil {
		b.edge(b.cur, f.continueTo, LabelContinue)
	} else {
		b.edge(b.cur, b.cfg.Blocks[b.cfg.Exit], LabelContinue)
	}
	b.cur = nil
}

// buildReturn handles return/raise/throw: the statement reads its
// expression and control transfers to the exit block.
func (b *builder) buildReturn(n *sitter.Node, kind string, label EdgeLabel) {
	b.ensureCur()
	b.addStmt(n, kind)
	b.edge(b.cur, b.cfg.Blocks[b.cfg.Exit], label)
	b.cur = nil
}

// ---------------------------------------------------------------------------
// try / except / finally
// ---------------------------------------------------------------------------

// handlerPart is one catch/except/rescue clause. headerNode carries
// the exception filter and binding; headerDefs marks the node as a
// pure binding (its identifiers are definitions, e.g. `catch (e)`).
type handlerPart struct {
	headerNode *sitter.Node
	headerDefs bool
	bodyNode   *sitter.Node
}

// tryParts feeds buildTry. The protected body is either one block
// node or an explicit statement list (Ruby's method-level rescue).
type tryParts struct {
	bodyNode    *sitter.Node
	bodyStmts   []*sitter.Node
	handlers    []handlerPart
	elseNode    *sitter.Node
	finallyNode *sitter.Node
}

// buildTry wires the protected region: every block created while
// building the body gets an exception edge to every handler — the
// conservative may-throw model (an exception can surface at any
// point of the region, so handler entry merges the region's defs).
// The region opens with an empty marker block so the region's IN
// state also reaches the handlers (an exception can fire before the
// first protected statement completes). Within one basic block the
// model stays block-granular: a def made and re-killed inside the
// same region block is not separately visible to the handler.
func (b *builder) buildTry(p tryParts) {
	b.ensureCur()
	tryBlock := b.newBlock("try")
	b.moveTo(tryBlock)
	regionStart := tryBlock.ID
	bodyBlock := b.newBlock("try_body")
	b.moveTo(bodyBlock)
	if p.bodyNode != nil {
		b.buildStmt(p.bodyNode)
	}
	for _, st := range p.bodyStmts {
		b.buildStmt(st)
	}
	tryEnd := b.cur
	regionEnd := len(b.cfg.Blocks)

	after := b.newBlock("try_end")
	handlerEnds := make([]*Block, 0, len(p.handlers))
	for _, h := range p.handlers {
		hb := b.newBlock("handler")
		for id := regionStart; id < regionEnd; id++ {
			b.edge(b.cfg.Blocks[id], hb, LabelException)
		}
		b.cur = hb
		if h.headerNode != nil {
			st := b.recordNode(h.headerNode, "catch")
			st.Defs, st.Uses = extractDefUse(b.spec, b.src, h.headerNode, h.headerDefs)
		}
		if h.bodyNode != nil {
			b.buildStmt(h.bodyNode)
		}
		handlerEnds = append(handlerEnds, b.cur)
	}

	mainEnd := tryEnd
	if p.elseNode != nil && tryEnd != nil {
		eb := b.newBlock("try_else")
		b.edge(tryEnd, eb, LabelSeq)
		b.cur = eb
		b.buildStmt(p.elseNode)
		mainEnd = b.cur
	}

	if p.finallyNode != nil {
		fb := b.newBlock("finally")
		if mainEnd != nil {
			b.edge(mainEnd, fb, LabelSeq)
		}
		for _, he := range handlerEnds {
			if he != nil {
				b.edge(he, fb, LabelFinally)
			}
		}
		// An exception that matches no handler (or a handler-less
		// try/finally) still runs the finalizer on its way out, so
		// the protected region always feeds the finally directly.
		for id := regionStart; id < regionEnd; id++ {
			b.edge(b.cfg.Blocks[id], fb, LabelException)
		}
		b.cur = fb
		b.buildStmt(p.finallyNode)
		if b.cur != nil {
			b.edge(b.cur, after, LabelSeq)
		}
	} else {
		if mainEnd != nil {
			b.edge(mainEnd, after, LabelSeq)
		}
		for _, he := range handlerEnds {
			if he != nil {
				b.edge(he, after, LabelSeq)
			}
		}
	}
	b.cur = after
}
