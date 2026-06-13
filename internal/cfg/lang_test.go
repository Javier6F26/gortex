package cfg

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Python
// ---------------------------------------------------------------------------

func TestPythonElifChainAndLoops(t *testing.T) {
	c := mustBuild(t, `def f(a):
    x = 0
    if a > 10:
        x = 1
    elif a > 5:
        x = 2
    else:
        x = 3
    while x > 0:
        x -= 1
        if x == 2:
            break
        else:
            continue
    return x
`, "python")

	for _, want := range []EdgeLabel{LabelTrue, LabelFalse, LabelLoopBack, LabelBreak, LabelContinue, LabelReturn} {
		if !hasEdgeLabel(c, want) {
			t.Errorf("missing %s edge", want)
		}
	}
	r := c.ReachingDefinitions()
	ret := stmtByText(t, c, "return x")
	ch := chainFor(t, r, ret.Index, "x")
	// All three branch defs and the loop decrement can reach the
	// return; the zeroth def is killed on every if path... but the
	// elif chain has an else, so x = 0 cannot survive — yet the
	// while may run zero times, so branch defs survive.
	d1 := stmtByText(t, c, "x = 1")
	d2 := stmtByText(t, c, "x = 2")
	d3 := stmtByText(t, c, "x = 3")
	dec := stmtByText(t, c, "x -= 1")
	for _, d := range []*Statement{d1, d2, d3, dec} {
		if !containsInt(ch.Defs, d.Index) {
			t.Errorf("def %q (stmt %d) should reach return: %v", d.Text, d.Index, ch.Defs)
		}
	}
	d0 := stmtByText(t, c, "x = 0")
	if containsInt(ch.Defs, d0.Index) {
		t.Errorf("x = 0 is killed on every if/elif/else path: %v", ch.Defs)
	}
	// Augmented assign reads its target.
	if len(dec.Uses) == 0 || dec.Uses[0] != "x" {
		t.Errorf("x -= 1 must use x: %v", dec.Uses)
	}
}

func TestPythonForAndTryExceptFinally(t *testing.T) {
	c := mustBuild(t, `def f(items):
    total = 0
    for i in items:
        total += i
    try:
        total = parse(total)
    except ValueError as e:
        total = -1
    finally:
        log(total)
    return total
`, "python")

	loop := stmtByText(t, c, "for i in items")
	if len(loop.Defs) != 1 || loop.Defs[0] != "i" {
		t.Errorf("for header must define i: %v", loop.Defs)
	}
	if len(loop.Uses) != 1 || loop.Uses[0] != "items" {
		t.Errorf("for header must use items: %v", loop.Uses)
	}
	if !hasEdgeLabel(c, LabelException) || !hasEdgeLabel(c, LabelFinally) {
		t.Fatalf("try/except/finally must wire exception+finally edges: %+v", c.Edges)
	}
	// The except binding defines e.
	catch := stmtByText(t, c, "ValueError as e")
	foundE := false
	for _, d := range catch.Defs {
		if d == "e" {
			foundE = true
		}
	}
	if !foundE {
		t.Errorf("except clause must define e: %v", catch.Defs)
	}
	// finally sees both the try def and the handler def.
	r := c.ReachingDefinitions()
	logStmt := stmtByText(t, c, "log(total)")
	ch := chainFor(t, r, logStmt.Index, "total")
	dTry := stmtByText(t, c, "total = parse(total)")
	dExc := stmtByText(t, c, "total = -1")
	if !containsInt(ch.Defs, dTry.Index) || !containsInt(ch.Defs, dExc.Index) {
		t.Errorf("finally must merge try and handler defs: %v", ch.Defs)
	}
	// An exception before the protected assignment leaves the
	// pre-try def live — it must reach the finally too.
	dInit := stmtByText(t, c, "total = 0")
	if !containsInt(ch.Defs, dInit.Index) {
		t.Errorf("pre-try def must reach the finally via the exception path: %v", ch.Defs)
	}
}

func TestPythonIndentedMethodDedents(t *testing.T) {
	// A method sliced out of a class body keeps its indentation —
	// Build must dedent before parsing.
	c := mustBuild(t, `    def m(self, a):
        x = a
        return x
`, "python")
	if c.FuncName != "m" {
		t.Errorf("FuncName = %q, want m", c.FuncName)
	}
	r := c.ReachingDefinitions()
	ret := stmtByText(t, c, "return x")
	if !hasChain(r, ret.Index, "x") {
		t.Errorf("dedented method must still produce chains")
	}
}

// Python match: arms live inside the match body block, not as direct
// children of match_statement. Each arm's pattern, body and chains
// must survive into the CFG, and capture patterns bind names.
func TestPythonMatchArmsAndCaptures(t *testing.T) {
	c := mustBuild(t, `def f(x):
    match x:
        case [a, b]:
            r = a + b
        case Point(px):
            r = px
        case _:
            r = 0
    return r
`, "python")

	if !hasEdgeLabel(c, LabelCase) {
		t.Fatalf("match arms must produce case edges; edges: %+v", c.Edges)
	}
	// The bodies of every arm must be present.
	for _, want := range []string{"r = a + b", "r = px", "r = 0"} {
		stmtByText(t, c, want)
	}
	// Capture patterns bind a, b and px as definitions; their uses
	// inside the arm bodies chain to the pattern.
	r := c.ReachingDefinitions()
	for use, vr := range map[string]string{"r = a + b": "a", "r = px": "px"} {
		st := stmtByText(t, c, use)
		if !hasChain(r, st.Index, vr) {
			t.Errorf("capture %q must chain into %q: chains %+v", vr, use, r.Chains)
		}
	}
	// All three arm defs of r reach the return.
	ret := stmtByText(t, c, "return r")
	ch := chainFor(t, r, ret.Index, "r")
	for _, def := range []string{"r = a + b", "r = px", "r = 0"} {
		d := stmtByText(t, c, def)
		if !containsInt(ch.Defs, d.Index) {
			t.Errorf("arm def %q must reach the return: %v", def, ch.Defs)
		}
	}
}

// Python match guard: the guard reads variables, it must not turn
// them into phantom definitions.
func TestPythonMatchGuardReadsNotDefines(t *testing.T) {
	c := mustBuild(t, `def f(x, lo):
    match x:
        case Box(v) if v > lo:
            return v
        case _:
            return 0
`, "python")
	pat := stmtByText(t, c, "Box(v)")
	if containsStr(pat.Defs, "lo") {
		t.Errorf("guard variable lo must not be a definition: %v", pat.Defs)
	}
	foundLo := false
	for _, u := range pat.Uses {
		if u == "lo" {
			foundLo = true
		}
	}
	if !foundLo {
		t.Errorf("guard must read lo: %v", pat.Uses)
	}
}

// Python for-else: a `break` skips the else clause, so the break-path
// definition is not killed by the else's reassignment.
func TestPythonForElseBreakSkipsElse(t *testing.T) {
	c := mustBuild(t, `def f(items, target):
    found = -1
    for i in items:
        if i == target:
            found = i
            break
    else:
        found = 0
    return found
`, "python")
	r := c.ReachingDefinitions()
	ret := stmtByText(t, c, "return found")
	ch := chainFor(t, r, ret.Index, "found")
	dBreak := stmtByText(t, c, "found = i")
	dElse := stmtByText(t, c, "found = 0")
	// The break-path def must survive to the return — break jumps past
	// the else, so the else cannot kill it.
	if !containsInt(ch.Defs, dBreak.Index) {
		t.Errorf("break-path def `found = i` must reach the return (break skips else): %v", ch.Defs)
	}
	// The else def reaches the return on the no-break path.
	if !containsInt(ch.Defs, dElse.Index) {
		t.Errorf("else def `found = 0` must reach the return on the normal exit: %v", ch.Defs)
	}
}

func containsStr(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// JavaScript / TypeScript
// ---------------------------------------------------------------------------

func TestJavaScriptSwitchFallthroughAndTry(t *testing.T) {
	c := mustBuild(t, `function f(a) {
  let x = 0;
  switch (a) {
    case 1:
      x = 1;
      break;
    case 2:
      x = 2;
    default:
      x = 3;
  }
  try {
    x = g(x);
  } catch (e) {
    x = -1;
  } finally {
    log(x);
  }
  return x;
}
`, "javascript")

	// case 2 falls through into default.
	s2 := stmtByText(t, c, "x = 2")
	s3 := stmtByText(t, c, "x = 3")
	if !edgeBetween(c, s2.Block, s3.Block, LabelSeq) {
		t.Errorf("case 2 must fall through to default: %+v", c.Edges)
	}
	// case 1 must NOT fall through (break).
	s1 := stmtByText(t, c, "x = 1")
	if edgeBetween(c, s1.Block, s2.Block, LabelSeq) {
		t.Errorf("case 1 ends with break and must not fall through")
	}
	if !hasEdgeLabel(c, LabelException) || !hasEdgeLabel(c, LabelFinally) {
		t.Fatalf("try/catch/finally edges missing")
	}
	r := c.ReachingDefinitions()
	ret := stmtByText(t, c, "return x")
	ch := chainFor(t, r, ret.Index, "x")
	dTry := stmtByText(t, c, "x = g(x)")
	dCatch := stmtByText(t, c, "x = -1")
	if !containsInt(ch.Defs, dTry.Index) || !containsInt(ch.Defs, dCatch.Index) {
		t.Errorf("return must merge try and catch defs: %v", ch.Defs)
	}
	// catch parameter defines e.
	for _, st := range c.Stmts {
		if st.Kind == "catch" {
			if len(st.Defs) != 1 || st.Defs[0] != "e" {
				t.Errorf("catch must define e: %v", st.Defs)
			}
		}
	}
}

func TestJavaScriptForOfAndLabeledBreak(t *testing.T) {
	c := mustBuild(t, `function f(arr) {
  let s = 0;
  outer: for (const v of arr) {
    for (let j = 0; j < v; j++) {
      if (j > 3) break outer;
      s += j;
    }
  }
  return s;
}
`, "javascript")
	hdr := stmtByText(t, c, "for (const v of arr)")
	if len(hdr.Defs) != 1 || hdr.Defs[0] != "v" {
		t.Errorf("for-of header must define v: %v", hdr.Defs)
	}
	if len(hdr.Uses) != 1 || hdr.Uses[0] != "arr" {
		t.Errorf("for-of header must use arr: %v", hdr.Uses)
	}
	br := stmtByText(t, c, "break outer")
	ret := stmtByText(t, c, "return s")
	var breakTo = -1
	for _, e := range c.Edges {
		if e.From == br.Block && e.Label == LabelBreak {
			breakTo = e.To
		}
	}
	ok := breakTo == ret.Block || edgeBetween(c, breakTo, ret.Block, LabelSeq)
	if !ok {
		t.Errorf("labeled break must exit the outer loop (got block %d)", breakTo)
	}
}

func TestTypeScriptMethodClassWrap(t *testing.T) {
	// A class method sliced out of its class doesn't parse
	// standalone — Build retries inside a synthetic class wrapper.
	c := mustBuild(t, `private compute(a: number): number {
  let x: number = a * 2;
  if (x > 10) {
    x = 10;
  }
  return x;
}
`, "typescript")
	if c.FuncName != "compute" {
		t.Errorf("FuncName = %q, want compute", c.FuncName)
	}
	r := c.ReachingDefinitions()
	ret := stmtByText(t, c, "return x")
	ch := chainFor(t, r, ret.Index, "x")
	if len(ch.Defs) != 2 {
		t.Errorf("both defs of x must reach the return: %v", ch.Defs)
	}
	// Line numbers must NOT be offset by the synthetic wrapper line.
	first := stmtByText(t, c, "let x")
	if first.StartLine != 2 {
		t.Errorf("wrapped parse must keep snippet-relative lines: got %d, want 2", first.StartLine)
	}
}

func TestJavaScriptDoWhile(t *testing.T) {
	c := mustBuild(t, `function f(n) {
  let i = 0;
  do {
    i++;
  } while (i < n);
  return i;
}
`, "javascript")
	if !hasEdgeLabel(c, LabelLoopBack) {
		t.Fatalf("do-while needs a loop_back edge: %+v", c.Edges)
	}
	// Post-test: the body executes before the condition; i++ must
	// reach the condition's use of i.
	r := c.ReachingDefinitions()
	cond := stmtByText(t, c, "i < n")
	inc := stmtByText(t, c, "i++")
	ch := chainFor(t, r, cond.Index, "i")
	if !containsInt(ch.Defs, inc.Index) {
		t.Errorf("do-while condition must see the body's def: %v", ch.Defs)
	}
}

// ---------------------------------------------------------------------------
// Java
// ---------------------------------------------------------------------------

func TestJavaMethodConstructsAndChains(t *testing.T) {
	c := mustBuild(t, `int f(int a) {
  int x = a + 1;
  for (int i = 0; i < a; i++) {
    if (i == 2) continue;
    if (i == 5) break;
    x += i;
  }
  switch (x) {
    case 1:
      x = 10;
      break;
    default:
      x = 20;
  }
  try {
    x = parse(x);
  } catch (Exception e) {
    x = 0;
  } finally {
    log(x);
  }
  return x;
}
`, "java")

	for _, want := range []EdgeLabel{LabelTrue, LabelFalse, LabelLoopBack, LabelBreak, LabelContinue, LabelCase, LabelException, LabelFinally, LabelReturn} {
		if !hasEdgeLabel(c, want) {
			t.Errorf("missing %s edge", want)
		}
	}
	r := c.ReachingDefinitions()
	ret := stmtByText(t, c, "return x")
	ch := chainFor(t, r, ret.Index, "x")
	dTry := stmtByText(t, c, "x = parse(x)")
	dCatch := stmtByText(t, c, "x = 0")
	if !containsInt(ch.Defs, dTry.Index) || !containsInt(ch.Defs, dCatch.Index) {
		t.Errorf("return must merge try and catch defs: %v", ch.Defs)
	}
	// Augmented assignment x += i reads and writes x.
	aug := stmtByText(t, c, "x += i")
	if len(aug.Defs) != 1 || aug.Defs[0] != "x" {
		t.Errorf("x += i defs: %v", aug.Defs)
	}
	wantUses := map[string]bool{"x": false, "i": false}
	for _, u := range aug.Uses {
		if _, ok := wantUses[u]; ok {
			wantUses[u] = true
		}
	}
	for v, seen := range wantUses {
		if !seen {
			t.Errorf("x += i must use %s: %v", v, aug.Uses)
		}
	}
}

func TestJavaEnhancedForHeader(t *testing.T) {
	c := mustBuild(t, `int sum(java.util.List<Integer> items) {
  int s = 0;
  for (Integer v : items) {
    s += v;
  }
  return s;
}
`, "java")
	hdr := stmtByText(t, c, "for (Integer v : items)")
	if len(hdr.Defs) != 1 || hdr.Defs[0] != "v" {
		t.Errorf("enhanced-for must define v: %v", hdr.Defs)
	}
	if len(hdr.Uses) != 1 || hdr.Uses[0] != "items" {
		t.Errorf("enhanced-for must use items: %v", hdr.Uses)
	}
}

// Java method-call names must not register as variable uses.
func TestJavaMethodNameNotAUse(t *testing.T) {
	c := mustBuild(t, `int f(java.util.List<Integer> list) {
  int size = 99;
  int n = list.size();
  return n + size;
}
`, "java")
	call := stmtByText(t, c, "int n = list.size()")
	for _, u := range call.Uses {
		if u == "size" {
			t.Errorf("the .size() method name must not count as a use of the local `size`: %v", call.Uses)
		}
	}
}

// Java arrow-form switch: `case 1 -> { … }` never falls through. The
// arm bodies must not be chained to one another by a phantom seq edge;
// the post-switch use must see every arm's def but no fallthrough.
func TestJavaArrowSwitchNoFallthrough(t *testing.T) {
	c := mustBuild(t, `int f(int x) {
  int y = 0;
  switch (x) {
    case 1 -> { y = 1; }
    case 2 -> { y = 2; }
    default -> { y = 3; }
  }
  return y;
}`, "java")
	s1 := stmtByText(t, c, "y = 1")
	s2 := stmtByText(t, c, "y = 2")
	s3 := stmtByText(t, c, "y = 3")
	// No fallthrough seq edges between arrow rules.
	if edgeBetween(c, s1.Block, s2.Block, LabelSeq) {
		t.Errorf("arrow rule `case 1` must not fall through to `case 2`; edges: %+v", c.Edges)
	}
	if edgeBetween(c, s2.Block, s3.Block, LabelSeq) {
		t.Errorf("arrow rule `case 2` must not fall through to default; edges: %+v", c.Edges)
	}
	// Every arm's def reaches the return (each via its own path).
	r := c.ReachingDefinitions()
	ret := stmtByText(t, c, "return y")
	ch := chainFor(t, r, ret.Index, "y")
	for _, d := range []*Statement{s1, s2, s3} {
		if !containsInt(ch.Defs, d.Index) {
			t.Errorf("arm def %q must reach the return: %v", d.Text, ch.Defs)
		}
	}
}

// ---------------------------------------------------------------------------
// Rust
// ---------------------------------------------------------------------------

func TestRustMatchLoopsAndChains(t *testing.T) {
	c := mustBuild(t, `fn f(a: i32) -> i32 {
    let mut x = a + 1;
    while x > 0 {
        x -= 1;
        if x == 2 { break; }
    }
    for i in 0..3 {
        x += i;
    }
    loop {
        x += 1;
        if x > 5 { break; }
    }
    match x {
        1 => { x = 10; }
        n => { x = n + 1; }
    }
    return x;
}
`, "rust")

	for _, want := range []EdgeLabel{LabelTrue, LabelFalse, LabelLoopBack, LabelBreak, LabelCase, LabelReturn} {
		if !hasEdgeLabel(c, want) {
			t.Errorf("missing %s edge", want)
		}
	}
	// The match binding pattern `n` defines n; the arm body uses it.
	r := c.ReachingDefinitions()
	armBody := stmtByText(t, c, "x = n + 1")
	ch := chainFor(t, r, armBody.Index, "n")
	pat := c.Stmts[ch.Defs[0]]
	if pat.Kind != "case" {
		t.Errorf("n's def should be the arm pattern statement, got kind %q (%q)", pat.Kind, pat.Text)
	}
	// for header defines i.
	hdr := stmtByText(t, c, "for i in 0..3")
	if len(hdr.Defs) != 1 || hdr.Defs[0] != "i" {
		t.Errorf("for header must define i: %v", hdr.Defs)
	}
}

// Rust labeled break: `break 'outer` must exit the outer loop, so the
// outer loop_end (holding `return s`) stays reachable. Before the fix
// the label never resolved and the break leaked to the innermost
// loop.
func TestRustLabeledBreakExitsOuter(t *testing.T) {
	c := mustBuild(t, `fn f() -> i32 {
    let mut s = 0;
    'outer: loop {
        loop {
            s += 1;
            if s > 3 { break 'outer; }
        }
    }
    return s;
}`, "rust")
	br := stmtByText(t, c, "break 'outer")
	var breakTo = -1
	for _, e := range c.Edges {
		if e.From == br.Block && e.Label == LabelBreak {
			breakTo = e.To
		}
	}
	if breakTo < 0 {
		t.Fatalf("no break edge from block %d", br.Block)
	}
	ret := stmtByText(t, c, "return s")
	ok := breakTo == ret.Block || edgeBetween(c, breakTo, ret.Block, LabelSeq)
	if !ok {
		t.Errorf("labeled break must exit the outer loop (block %d), reaching the return block %d; edges: %+v", breakTo, ret.Block, c.Edges)
	}
	// The label identifier must not leak in as a variable use.
	if containsStr(br.Uses, "outer") {
		t.Errorf("loop label `outer` must not register as a use: %v", br.Uses)
	}
}

// Rust match guard: the guard condition lives on the match_pattern as
// its `condition` field; its reads must not become phantom binding
// definitions that kill the real parameter def.
func TestRustMatchGuardReadsNotDefines(t *testing.T) {
	c := mustBuild(t, `fn f(o: Option<i32>, z: i32) -> i32 {
    match o {
        Some(y) if z > 0 => y,
        _ => z,
    }
}`, "rust")
	pat := stmtByText(t, c, "Some(y)")
	// z is read by the guard, not bound by the pattern.
	if containsStr(pat.Defs, "z") {
		t.Errorf("guard variable z must not be a pattern definition: %v", pat.Defs)
	}
	foundZ := false
	for _, u := range pat.Uses {
		if u == "z" {
			foundZ = true
		}
	}
	if !foundZ {
		t.Errorf("guard must read z: %v", pat.Uses)
	}
	// The capture y is still a definition.
	if !containsStr(pat.Defs, "y") {
		t.Errorf("capture y must be a definition: %v", pat.Defs)
	}
}

func TestRustIfLetBindsInHeader(t *testing.T) {
	c := mustBuild(t, `fn f(opt: Option<i32>) -> i32 {
    let mut x = 0;
    if let Some(v) = opt {
        x = v;
    }
    return x;
}
`, "rust")
	cond := stmtByText(t, c, "let Some(v) = opt")
	foundV := false
	for _, d := range cond.Defs {
		if d == "v" {
			foundV = true
		}
	}
	if !foundV {
		t.Errorf("if-let must define v in the header: defs=%v", cond.Defs)
	}
	r := c.ReachingDefinitions()
	use := stmtByText(t, c, "x = v")
	if !hasChain(r, use.Index, "v") {
		t.Errorf("v's use must chain to the if-let binding")
	}
}

// ---------------------------------------------------------------------------
// Ruby
// ---------------------------------------------------------------------------

func TestRubyConstructsAndChains(t *testing.T) {
	c := mustBuild(t, `def f(a)
  x = a + 1
  if x > 10
    x = 10
  elsif x > 5
    x = 5
  else
    x = 0
  end
  while x > 0
    x -= 1
    break if x == 2
  end
  case x
  when 1
    x = 100
  else
    x = 200
  end
  return x
end
`, "ruby")

	for _, want := range []EdgeLabel{LabelTrue, LabelFalse, LabelLoopBack, LabelBreak, LabelCase, LabelReturn} {
		if !hasEdgeLabel(c, want) {
			t.Errorf("missing %s edge", want)
		}
	}
	r := c.ReachingDefinitions()
	ret := stmtByText(t, c, "return x")
	ch := chainFor(t, r, ret.Index, "x")
	d100 := stmtByText(t, c, "x = 100")
	d200 := stmtByText(t, c, "x = 200")
	if !containsInt(ch.Defs, d100.Index) || !containsInt(ch.Defs, d200.Index) {
		t.Errorf("case-arm defs must union at the return: %v", ch.Defs)
	}
}

func TestRubyRescueEnsureAndBlocks(t *testing.T) {
	c := mustBuild(t, `def f(items)
  total = 0
  items.each do |it|
    total += it
    next if it < 0
  end
  begin
    total = parse(total)
  rescue ArgumentError => e
    total = -1
  ensure
    log(total)
  end
  total
end
`, "ruby")

	if !hasEdgeLabel(c, LabelException) || !hasEdgeLabel(c, LabelFinally) {
		t.Fatalf("begin/rescue/ensure must wire exception+finally edges")
	}
	// The block call models a loop: block param defined, loop_back
	// present, `next` is a continue.
	if !hasEdgeLabel(c, LabelLoopBack) || !hasEdgeLabel(c, LabelContinue) {
		t.Errorf("each-block must model loop_back and continue: %+v", c.Edges)
	}
	var paramStmt *Statement
	for _, st := range c.Stmts {
		if st.Kind == "param" && len(st.Defs) == 1 && st.Defs[0] == "it" {
			paramStmt = st
		}
	}
	if paramStmt == nil {
		t.Fatalf("block parameter |it| must be a definition")
	}
	r := c.ReachingDefinitions()
	use := stmtByText(t, c, "total += it")
	ch := chainFor(t, r, use.Index, "it")
	if !containsInt(ch.Defs, paramStmt.Index) {
		t.Errorf("it's use must chain to the block parameter: %v", ch.Defs)
	}
	// rescue binding defines e.
	foundE := false
	for _, st := range c.Stmts {
		if st.Kind == "catch" {
			for _, d := range st.Defs {
				if d == "e" {
					foundE = true
				}
			}
		}
	}
	if !foundE {
		t.Errorf("rescue => e must define e")
	}
	// ensure merges try + rescue defs.
	logStmt := stmtByText(t, c, "log(total)")
	chT := chainFor(t, r, logStmt.Index, "total")
	dTry := stmtByText(t, c, "total = parse(total)")
	dResc := stmtByText(t, c, "total = -1")
	if !containsInt(chT.Defs, dTry.Index) || !containsInt(chT.Defs, dResc.Index) {
		t.Errorf("ensure must merge try and rescue defs: %v", chT.Defs)
	}
}

func TestRubyUnlessAndModifiers(t *testing.T) {
	c := mustBuild(t, `def f(x)
  y = 0
  unless x > 0
    y = 1
  end
  y += 2 if x == 5
  return y
end
`, "ruby")
	r := c.ReachingDefinitions()
	ret := stmtByText(t, c, "return y")
	ch := chainFor(t, r, ret.Index, "y")
	d0 := stmtByText(t, c, "y = 0")
	d1 := stmtByText(t, c, "y = 1")
	dMod := stmtByText(t, c, "y += 2")
	for _, d := range []*Statement{d0, d1, dMod} {
		if !containsInt(ch.Defs, d.Index) {
			t.Errorf("def %q must reach return (conditional paths): %v", d.Text, ch.Defs)
		}
	}
}

// ---------------------------------------------------------------------------
// nested construct stress: every language parses a nested
// if-in-loop-in-if shape and produces a connected graph.
// ---------------------------------------------------------------------------

func TestNestedShapesAllLanguages(t *testing.T) {
	cases := map[string]string{
		"go": `func f(a int) int {
	r := 0
	if a > 0 {
		for i := 0; i < a; i++ {
			if i%2 == 0 {
				r += i
			} else {
				r -= i
			}
		}
	}
	return r
}`,
		"python": `def f(a):
    r = 0
    if a > 0:
        for i in range(a):
            if i % 2 == 0:
                r += i
            else:
                r -= i
    return r
`,
		"javascript": `function f(a) {
  let r = 0;
  if (a > 0) {
    for (let i = 0; i < a; i++) {
      if (i % 2 === 0) { r += i; } else { r -= i; }
    }
  }
  return r;
}`,
		"typescript": `function f(a: number): number {
  let r = 0;
  if (a > 0) {
    for (let i = 0; i < a; i++) {
      if (i % 2 === 0) { r += i; } else { r -= i; }
    }
  }
  return r;
}`,
		"java": `int f(int a) {
  int r = 0;
  if (a > 0) {
    for (int i = 0; i < a; i++) {
      if (i % 2 == 0) { r += i; } else { r -= i; }
    }
  }
  return r;
}`,
		"rust": `fn f(a: i32) -> i32 {
    let mut r = 0;
    if a > 0 {
        for i in 0..a {
            if i % 2 == 0 { r += i; } else { r -= i; }
        }
    }
    return r;
}`,
		"ruby": `def f(a)
  r = 0
  if a > 0
    for i in 0..a
      if i % 2 == 0
        r += i
      else
        r -= i
      end
    end
  end
  return r
end
`,
	}
	for lang, src := range cases {
		t.Run(lang, func(t *testing.T) {
			c := mustBuild(t, src, lang)
			if len(c.Blocks) < 6 {
				t.Fatalf("%s: nested shape should produce several blocks, got %d", lang, len(c.Blocks))
			}
			if !hasEdgeLabel(c, LabelLoopBack) {
				t.Errorf("%s: missing loop_back", lang)
			}
			r := c.ReachingDefinitions()
			ret := stmtByText(t, c, "return r")
			ch := chainFor(t, r, ret.Index, "r")
			dPlus := stmtByText(t, c, "r += i")
			dMinus := stmtByText(t, c, "r -= i")
			dInit := c.Stmts[0]
			for _, st := range c.Stmts {
				if strings.Contains(st.Text, "r = 0") || strings.Contains(st.Text, "let r = 0") || strings.Contains(st.Text, "int r = 0") || strings.Contains(st.Text, "let mut r = 0") || strings.Contains(st.Text, "r := 0") {
					dInit = st
					break
				}
			}
			for _, d := range []*Statement{dInit, dPlus, dMinus} {
				if !containsInt(ch.Defs, d.Index) {
					t.Errorf("%s: def %q must reach the return: %v", lang, d.Text, ch.Defs)
				}
			}
			// Every non-entry block with statements must be reachable
			// from somewhere except deliberately-unreachable ones.
			seenTarget := map[int]bool{c.Entry: true}
			for _, e := range c.Edges {
				seenTarget[e.To] = true
			}
			for _, bl := range c.Blocks {
				if bl.ID == c.Entry || len(bl.Stmts) == 0 {
					continue
				}
				if !seenTarget[bl.ID] && bl.Label != "unreachable" {
					t.Errorf("%s: block %d (%s) has statements but no incoming edge", lang, bl.ID, bl.Label)
				}
			}
		})
	}
}
