package cfg

import (
	"strings"
	"testing"
)

// mustBuild builds a CFG and fails the test on error.
func mustBuild(t *testing.T, src, lang string) *CFG {
	t.Helper()
	c, err := Build([]byte(src), lang, Options{})
	if err != nil {
		t.Fatalf("Build(%s): %v", lang, err)
	}
	return c
}

// stmtByText finds the first statement whose text contains sub.
func stmtByText(t *testing.T, c *CFG, sub string) *Statement {
	t.Helper()
	for _, st := range c.Stmts {
		if strings.Contains(st.Text, sub) {
			return st
		}
	}
	t.Fatalf("no statement containing %q; have: %v", sub, stmtTexts(c))
	return nil
}

func stmtTexts(c *CFG) []string {
	out := make([]string, len(c.Stmts))
	for i, st := range c.Stmts {
		out[i] = st.Text
	}
	return out
}

// hasEdge reports whether an edge with the label connects the blocks
// holding the two statements (or block IDs when from/to are ints).
func hasEdgeLabel(c *CFG, label EdgeLabel) bool {
	for _, e := range c.Edges {
		if e.Label == label {
			return true
		}
	}
	return false
}

func edgeBetween(c *CFG, from, to int, label EdgeLabel) bool {
	for _, e := range c.Edges {
		if e.From == from && e.To == to && e.Label == label {
			return true
		}
	}
	return false
}

// chainFor returns the def→use chain for (use statement, var).
func chainFor(t *testing.T, r *ReachingResult, stmt int, v string) UseChain {
	t.Helper()
	for _, ch := range r.Chains {
		if ch.Stmt == stmt && ch.Var == v {
			return ch
		}
	}
	t.Fatalf("no chain for stmt=%d var=%q; chains: %+v", stmt, v, r.Chains)
	return UseChain{}
}

func hasChain(r *ReachingResult, stmt int, v string) bool {
	for _, ch := range r.Chains {
		if ch.Stmt == stmt && ch.Var == v {
			return true
		}
	}
	return false
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// construction basics
// ---------------------------------------------------------------------------

func TestBuildGoIfElseDiamond(t *testing.T) {
	c := mustBuild(t, `
func f(a int) int {
	x := 1
	if a > 0 {
		x = 2
	} else {
		x = 3
	}
	return x
}
`, "go")

	if c.FuncName != "f" {
		t.Errorf("FuncName = %q, want f", c.FuncName)
	}
	cond := stmtByText(t, c, "a > 0")
	if cond.Kind != "cond" {
		t.Errorf("condition kind = %q, want cond", cond.Kind)
	}
	// The diamond: cond block branches true and false, both sides
	// rejoin before the return.
	trueTo, falseTo := -1, -1
	for _, e := range c.Edges {
		if e.From == cond.Block && e.Label == LabelTrue {
			trueTo = e.To
		}
		if e.From == cond.Block && e.Label == LabelFalse {
			falseTo = e.To
		}
	}
	if trueTo < 0 || falseTo < 0 {
		t.Fatalf("missing branch edges from cond block %d: %+v", cond.Block, c.Edges)
	}
	thenStmt := stmtByText(t, c, "x = 2")
	elseStmt := stmtByText(t, c, "x = 3")
	if thenStmt.Block != trueTo {
		t.Errorf("then statement in block %d, want %d", thenStmt.Block, trueTo)
	}
	if elseStmt.Block != falseTo {
		t.Errorf("else statement in block %d, want %d", elseStmt.Block, falseTo)
	}
	ret := stmtByText(t, c, "return x")
	if ret.Block == thenStmt.Block || ret.Block == elseStmt.Block {
		t.Errorf("return must live in the join block, not a branch arm")
	}
	if !edgeBetween(c, ret.Block, c.Exit, LabelReturn) {
		t.Errorf("missing return edge to exit")
	}
}

func TestBuildGoLoopBreakContinue(t *testing.T) {
	c := mustBuild(t, `
func f(n int) int {
	s := 0
	for i := 0; i < n; i++ {
		if i == 3 {
			continue
		}
		if i == 7 {
			break
		}
		s += i
	}
	return s
}
`, "go")

	for _, want := range []EdgeLabel{LabelLoopBack, LabelBreak, LabelContinue, LabelTrue, LabelFalse} {
		if !hasEdgeLabel(c, want) {
			t.Errorf("missing %s edge; edges: %+v", want, c.Edges)
		}
	}
	// continue must target the update block (i++ still runs), not
	// skip it.
	contStmt := stmtByText(t, c, "continue")
	upd := stmtByText(t, c, "i++")
	if !edgeBetween(c, contStmt.Block, upd.Block, LabelContinue) {
		t.Errorf("continue should target the loop update block %d; edges: %+v", upd.Block, c.Edges)
	}
	// The update's defs: i (and a use of i).
	if len(upd.Defs) != 1 || upd.Defs[0] != "i" {
		t.Errorf("update defs = %v, want [i]", upd.Defs)
	}
	if len(upd.Uses) != 1 || upd.Uses[0] != "i" {
		t.Errorf("update uses = %v, want [i]", upd.Uses)
	}
}

func TestBuildGoSwitchFallthrough(t *testing.T) {
	c := mustBuild(t, `
func f(x int) int {
	y := 0
	switch x {
	case 1:
		y = 1
		fallthrough
	case 2:
		y = 2
	default:
		y = 3
	}
	return y
}
`, "go")

	if !hasEdgeLabel(c, LabelCase) {
		t.Fatalf("missing case edges")
	}
	// fallthrough: the block holding `y = 1` flows into the block
	// holding `y = 2` sequentially.
	s1 := stmtByText(t, c, "y = 1")
	s2 := stmtByText(t, c, "y = 2")
	if !edgeBetween(c, s1.Block, s2.Block, LabelSeq) {
		t.Errorf("missing fallthrough seq edge %d->%d; edges: %+v", s1.Block, s2.Block, c.Edges)
	}
	// With a default case there must be no unmatched-subject edge.
	cond := stmtByText(t, c, "x")
	if cond.Kind != "cond" {
		cond = c.Stmts[1]
	}
	for _, e := range c.Edges {
		if e.From == cond.Block && e.Label == LabelFalse {
			t.Errorf("switch with default must not emit a false edge")
		}
	}
}

func TestBuildGoLabeledBreak(t *testing.T) {
	c := mustBuild(t, `
func f() int {
	s := 0
outer:
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			if j == 2 {
				break outer
			}
			s++
		}
	}
	return s
}
`, "go")

	br := stmtByText(t, c, "break outer")
	// The labeled break must exit the OUTER loop: its target block
	// must be the block holding `return s` (outer loop_end flows
	// there) — concretely, the break edge must not target the inner
	// loop's end.
	var breakTo = -1
	for _, e := range c.Edges {
		if e.From == br.Block && e.Label == LabelBreak {
			breakTo = e.To
		}
	}
	if breakTo < 0 {
		t.Fatalf("no break edge from %d", br.Block)
	}
	ret := stmtByText(t, c, "return s")
	// outer loop_end may be empty and flow to the return's block;
	// accept either the return block itself or a block that reaches
	// it via one seq hop.
	ok := breakTo == ret.Block || edgeBetween(c, breakTo, ret.Block, LabelSeq)
	if !ok {
		t.Errorf("labeled break targets block %d, expected the outer loop end (return block %d)", breakTo, ret.Block)
	}
}

func TestBuildGoDeferNoted(t *testing.T) {
	c := mustBuild(t, `
func f() {
	defer cleanup()
	work()
}
`, "go")
	d := stmtByText(t, c, "defer cleanup()")
	if d.Kind != "defer" {
		t.Errorf("defer kind = %q, want defer", d.Kind)
	}
	w := stmtByText(t, c, "work()")
	if w.Block != d.Block {
		t.Errorf("defer must not split the basic block: defer in %d, work in %d", d.Block, w.Block)
	}
}

func TestBuildGoInfiniteLoop(t *testing.T) {
	c := mustBuild(t, `
func f() {
	for {
		if done() {
			break
		}
	}
}
`, "go")
	if !hasEdgeLabel(c, LabelLoopBack) || !hasEdgeLabel(c, LabelBreak) {
		t.Fatalf("infinite loop needs loop_back and break edges: %+v", c.Edges)
	}
}

// blockLabelByID returns a block's label.
func blockLabelByID(c *CFG, id int) string { return c.Blocks[id].Label }

// breakEdgeTarget returns the block a break statement's break edge
// targets, or -1.
func breakEdgeTarget(c *CFG, br *Statement) int {
	for _, e := range c.Edges {
		if e.From == br.Block && e.Label == LabelBreak {
			return e.To
		}
	}
	return -1
}

// A `break` inside a Go select must exit the select, not the
// enclosing loop. With the fix the break targets the switch_end
// block; without it the break leaked to the loop / function exit and
// the statement after the select became unreachable.
func TestBuildGoSelectBreakExitsSelect(t *testing.T) {
	c := mustBuild(t, `func f(ch chan int) int {
	s := 0
	for {
		select {
		case v := <-ch:
			s += v
			break
		}
		s++
	}
	return s
}`, "go")
	br := stmtByText(t, c, "break")
	to := breakEdgeTarget(c, br)
	if to < 0 {
		t.Fatalf("no break edge from block %d; edges: %+v", br.Block, c.Edges)
	}
	if got := blockLabelByID(c, to); got != "switch_end" {
		t.Errorf("select break targets %q block %d, want the switch_end; edges: %+v", got, to, c.Edges)
	}
	// `s++` after the select must stay reachable from the select's
	// merge point — the loop-carried `s` must reach the final use.
	r := c.ReachingDefinitions()
	inc := stmtByText(t, c, "s++")
	chS := chainFor(t, r, inc.Index, "s")
	if len(chS.Defs) == 0 {
		t.Errorf("s++ must see a prior def of s: %v", chS.Defs)
	}
}

// A labeled Go switch: `break L` must resolve to the switch end so
// the post-switch use sees the in-case definition. Before the fix the
// labeled break leaked to the function exit, so `s = 1` never reached
// `return s`.
func TestBuildGoLabeledSwitchBreak(t *testing.T) {
	c := mustBuild(t, `func f(x int) int {
	s := 0
	L:
	switch x {
	case 1:
		s = 1
		break L
	}
	return s
}`, "go")
	br := stmtByText(t, c, "break L")
	to := breakEdgeTarget(c, br)
	if to < 0 {
		t.Fatalf("no break edge from block %d", br.Block)
	}
	if got := blockLabelByID(c, to); got != "switch_end" {
		t.Errorf("labeled switch break targets %q, want switch_end; edges: %+v", got, c.Edges)
	}
	r := c.ReachingDefinitions()
	ret := stmtByText(t, c, "return s")
	ch := chainFor(t, r, ret.Index, "s")
	d1 := stmtByText(t, c, "s = 1")
	if !containsInt(ch.Defs, d1.Index) {
		t.Errorf("in-case def `s = 1` must reach the return via the labeled break: %v", ch.Defs)
	}
}

// A Go type switch `switch v := x.(type)` must define the alias v;
// every use of v inside the cases must chain back to it.
func TestBuildGoTypeSwitchAliasDefines(t *testing.T) {
	c := mustBuild(t, `func f(x interface{}) int {
	switch v := x.(type) {
	case int:
		return v
	default:
		return 0
	}
}`, "go")
	r := c.ReachingDefinitions()
	use := stmtByText(t, c, "return v")
	ch := chainFor(t, r, use.Index, "v")
	if len(ch.Defs) == 0 {
		t.Fatalf("type-switch alias v must produce a chain at its use: %v", ch.Defs)
	}
	def := c.Stmts[ch.Defs[0]]
	if def.Kind != "cond" {
		t.Errorf("v's def should be the type-switch cond statement, got kind %q (%q)", def.Kind, def.Text)
	}
	foundV := false
	for _, d := range def.Defs {
		if d == "v" {
			foundV = true
		}
	}
	if !foundV {
		t.Errorf("type-switch cond must define v: %v", def.Defs)
	}
	// The switched expression x is a read, not a binding.
	if containsString(def.Defs, "x") {
		t.Errorf("the switched value x must not be a definition: %v", def.Defs)
	}
}

func containsString(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// reaching definitions — textbook shapes
// ---------------------------------------------------------------------------

// Redefinition kills: the second assignment must be the only def
// reaching the final use.
func TestReachingRedefinitionKills(t *testing.T) {
	c := mustBuild(t, `
func f() int {
	x := 1
	x = 2
	return x
}
`, "go")
	r := c.ReachingDefinitions()
	def1 := stmtByText(t, c, "x := 1")
	def2 := stmtByText(t, c, "x = 2")
	ret := stmtByText(t, c, "return x")
	ch := chainFor(t, r, ret.Index, "x")
	if containsInt(ch.Defs, def1.Index) {
		t.Errorf("killed definition %d still reaches the use: %v", def1.Index, ch.Defs)
	}
	if !containsInt(ch.Defs, def2.Index) {
		t.Errorf("live definition %d does not reach the use: %v", def2.Index, ch.Defs)
	}
}

// Branch merge unions: both arm definitions reach the post-join use.
func TestReachingBranchMergeUnion(t *testing.T) {
	c := mustBuild(t, `
func f(a bool) int {
	x := 0
	if a {
		x = 1
	} else {
		x = 2
	}
	return x
}
`, "go")
	r := c.ReachingDefinitions()
	d1 := stmtByText(t, c, "x = 1")
	d2 := stmtByText(t, c, "x = 2")
	d0 := stmtByText(t, c, "x := 0")
	ret := stmtByText(t, c, "return x")
	ch := chainFor(t, r, ret.Index, "x")
	if !containsInt(ch.Defs, d1.Index) || !containsInt(ch.Defs, d2.Index) {
		t.Errorf("branch-arm defs must union at the join: %v (want %d and %d)", ch.Defs, d1.Index, d2.Index)
	}
	if containsInt(ch.Defs, d0.Index) {
		t.Errorf("pre-branch def %d is killed on every path and must not reach: %v", d0.Index, ch.Defs)
	}
}

// One-armed if: the initial def survives the merge alongside the arm
// def.
func TestReachingOneArmedIfKeepsBoth(t *testing.T) {
	c := mustBuild(t, `
func f(a bool) int {
	x := 0
	if a {
		x = 1
	}
	return x
}
`, "go")
	r := c.ReachingDefinitions()
	d0 := stmtByText(t, c, "x := 0")
	d1 := stmtByText(t, c, "x = 1")
	ret := stmtByText(t, c, "return x")
	ch := chainFor(t, r, ret.Index, "x")
	if !containsInt(ch.Defs, d0.Index) || !containsInt(ch.Defs, d1.Index) {
		t.Errorf("one-armed if must keep both defs at the join: %v", ch.Defs)
	}
}

// Loop-carried defs: a def at the loop bottom reaches the use at the
// loop top on the next iteration.
func TestReachingLoopCarried(t *testing.T) {
	c := mustBuild(t, `
func f(n int) int {
	s := 0
	for i := 0; i < n; i++ {
		s = s + i
	}
	return s
}
`, "go")
	r := c.ReachingDefinitions()
	d0 := stmtByText(t, c, "s := 0")
	dLoop := stmtByText(t, c, "s = s + i")
	// The loop body's use of s sees both the init def and its own
	// previous-iteration def.
	ch := chainFor(t, r, dLoop.Index, "s")
	if !containsInt(ch.Defs, d0.Index) {
		t.Errorf("init def must reach the first iteration: %v", ch.Defs)
	}
	if !containsInt(ch.Defs, dLoop.Index) {
		t.Errorf("loop-carried def must reach the next iteration: %v", ch.Defs)
	}
	// The condition's use of i sees the init AND the increment.
	cond := stmtByText(t, c, "i < n")
	chI := chainFor(t, r, cond.Index, "i")
	init := stmtByText(t, c, "i := 0")
	inc := stmtByText(t, c, "i++")
	if !containsInt(chI.Defs, init.Index) || !containsInt(chI.Defs, inc.Index) {
		t.Errorf("loop condition must see init and increment defs of i: %v", chI.Defs)
	}
	// And the final use of s unions init + loop defs.
	ret := stmtByText(t, c, "return s")
	chRet := chainFor(t, r, ret.Index, "s")
	if !containsInt(chRet.Defs, d0.Index) || !containsInt(chRet.Defs, dLoop.Index) {
		t.Errorf("post-loop use must union zero-trip and loop defs: %v", chRet.Defs)
	}
}

// Parameters are block-0 definitions reaching every unshadowed use.
func TestReachingParamsReachUses(t *testing.T) {
	c := mustBuild(t, `
func f(a int, b int) int {
	x := a + b
	return x
}
`, "go")
	r := c.ReachingDefinitions()
	assign := stmtByText(t, c, "x := a + b")
	chA := chainFor(t, r, assign.Index, "a")
	if len(chA.Defs) != 1 {
		t.Fatalf("param a should have exactly one def: %v", chA.Defs)
	}
	paramStmt := c.Stmts[chA.Defs[0]]
	if paramStmt.Kind != "param" {
		t.Errorf("a's def should be the synthetic param statement, got kind %q", paramStmt.Kind)
	}
	if paramStmt.Block != c.Entry {
		t.Errorf("param defs live in the entry block %d, got %d", c.Entry, paramStmt.Block)
	}
}

// A use with no defining statement (package global) produces no chain.
func TestReachingGlobalsHaveNoChain(t *testing.T) {
	c := mustBuild(t, `
func f() int {
	return globalCounter
}
`, "go")
	r := c.ReachingDefinitions()
	ret := stmtByText(t, c, "return globalCounter")
	if hasChain(r, ret.Index, "globalCounter") {
		t.Errorf("global reads must not produce chains")
	}
}

// Early return prunes: a def after the return in the same branch
// can't reach uses before it.
func TestReachingEarlyReturnIsolation(t *testing.T) {
	c := mustBuild(t, `
func f(a bool) int {
	x := 1
	if a {
		return x
	}
	x = 2
	return x
}
`, "go")
	r := c.ReachingDefinitions()
	d1 := stmtByText(t, c, "x := 1")
	d2 := stmtByText(t, c, "x = 2")
	stmts := []*Statement{}
	for _, st := range c.Stmts {
		if strings.Contains(st.Text, "return x") {
			stmts = append(stmts, st)
		}
	}
	if len(stmts) != 2 {
		t.Fatalf("want two return statements, got %d", len(stmts))
	}
	early, late := stmts[0], stmts[1]
	chEarly := chainFor(t, r, early.Index, "x")
	if containsInt(chEarly.Defs, d2.Index) {
		t.Errorf("def after the early return must not reach it: %v", chEarly.Defs)
	}
	if !containsInt(chEarly.Defs, d1.Index) {
		t.Errorf("initial def must reach the early return: %v", chEarly.Defs)
	}
	chLate := chainFor(t, r, late.Index, "x")
	if !containsInt(chLate.Defs, d2.Index) || containsInt(chLate.Defs, d1.Index) {
		t.Errorf("late return sees only the redefinition: %v", chLate.Defs)
	}
}

// Statement granularity: two uses of the same variable in different
// statements get independent chains.
func TestReachingStatementGranularity(t *testing.T) {
	c := mustBuild(t, `
func f() int {
	x := 1
	y := x
	x = 2
	z := x
	return y + z
}
`, "go")
	r := c.ReachingDefinitions()
	d1 := stmtByText(t, c, "x := 1")
	d2 := stmtByText(t, c, "x = 2")
	useY := stmtByText(t, c, "y := x")
	useZ := stmtByText(t, c, "z := x")
	chY := chainFor(t, r, useY.Index, "x")
	chZ := chainFor(t, r, useZ.Index, "x")
	if !containsInt(chY.Defs, d1.Index) || containsInt(chY.Defs, d2.Index) {
		t.Errorf("y := x must see only the first def: %v", chY.Defs)
	}
	if !containsInt(chZ.Defs, d2.Index) || containsInt(chZ.Defs, d1.Index) {
		t.Errorf("z := x must see only the second def: %v", chZ.Defs)
	}
}

// Closures are opaque: assignments inside a func literal don't
// define in the enclosing frame.
func TestClosureOpaque(t *testing.T) {
	c := mustBuild(t, `
func f() int {
	x := 1
	g := func() {
		x = 99
	}
	g()
	return x
}
`, "go")
	r := c.ReachingDefinitions()
	ret := stmtByText(t, c, "return x")
	ch := chainFor(t, r, ret.Index, "x")
	d1 := stmtByText(t, c, "x := 1")
	if len(ch.Defs) != 1 || ch.Defs[0] != d1.Index {
		t.Errorf("closure write must not count as an enclosing-scope def: %v", ch.Defs)
	}
}

// ---------------------------------------------------------------------------
// options / rendering
// ---------------------------------------------------------------------------

func TestLineOffsetShiftsLines(t *testing.T) {
	src := `func f() int {
	x := 1
	return x
}`
	c, err := Build([]byte(src), "go", Options{LineOffset: 99})
	if err != nil {
		t.Fatal(err)
	}
	st := stmtByText(t, c, "x := 1")
	if st.StartLine != 101 {
		t.Errorf("StartLine = %d, want 101 (snippet line 2 + offset 99)", st.StartLine)
	}
}

func TestMermaidRendering(t *testing.T) {
	c := mustBuild(t, `
func f(a int) int {
	if a > 0 {
		return 1
	}
	return 2
}
`, "go")
	m := c.Mermaid()
	if !strings.HasPrefix(m, "flowchart TD") {
		t.Errorf("mermaid must start with flowchart TD: %q", m)
	}
	if !strings.Contains(m, "-->|true|") || !strings.Contains(m, "-->|false|") {
		t.Errorf("mermaid must label branch edges: %s", m)
	}
	if !strings.Contains(m, "entry") || !strings.Contains(m, "exit") {
		t.Errorf("mermaid must render entry/exit: %s", m)
	}
}

func TestUnsupportedLanguage(t *testing.T) {
	if _, err := Build([]byte("x"), "cobol", Options{}); err == nil {
		t.Fatal("expected error for unsupported language")
	}
	if SupportedLanguage("cobol") {
		t.Fatal("cobol must not be supported")
	}
	if !SupportedLanguage("go") || !SupportedLanguage("ruby") {
		t.Fatal("go and ruby must be supported")
	}
}

func TestNoFunctionInSource(t *testing.T) {
	if _, err := Build([]byte("var x = 1"), "go", Options{}); err == nil {
		t.Fatal("expected error when source holds no function")
	}
}
