package languages

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

// callReturnUsage returns the stamped return-usage label of the first
// EdgeCalls edge targeting callee. The empty string means the edge
// exists but carries no label.
func callReturnUsage(t *testing.T, edges []*graph.Edge, callee string) string {
	t.Helper()
	for _, e := range edges {
		if e.Kind != graph.EdgeCalls {
			continue
		}
		if strings.HasSuffix(e.To, "::"+callee) || strings.HasSuffix(e.To, "*."+callee) {
			return graph.ReturnUsageOf(e)
		}
	}
	t.Fatalf("no call edge to %q found", callee)
	return ""
}

func TestGoReturnUsage(t *testing.T) {
	src := []byte(`package main

func caller() (int, error) {
	f1()
	x := f2()
	var v = f3()
	y = f4()
	a, _ := f5()
	_, _ = f6()
	go f7()
	defer f8()
	g(f9())
	if f10() {
	}
	for f11() {
	}
	switch f12() {
	}
	f13().Method()
	ch <- f14()
	for _, item := range f15() {
	}
	if err := f16(); err != nil {
	}
	m := T{Field: f17()}
	return f18()
}
`)
	e := NewGoExtractor()
	result, err := e.Extract("main.go", src)
	require.NoError(t, err)
	defer result.Tree.Release()

	for callee, want := range map[string]string{
		"f1":  graph.ReturnUsageDiscarded,
		"f2":  graph.ReturnUsageAssigned,
		"f3":  graph.ReturnUsageAssigned,
		"f4":  graph.ReturnUsageAssigned,
		"f5":  graph.ReturnUsagePartiallyIgnored,
		"f6":  graph.ReturnUsageDiscarded, // every sink blank — value thrown away
		"f7":  graph.ReturnUsageGoroutine,
		"f8":  graph.ReturnUsageDeferred,
		"f9":  graph.ReturnUsageArgument,
		"f10": graph.ReturnUsageCondition,
		"f11": graph.ReturnUsageCondition,
		"f12": graph.ReturnUsageCondition,
		"f13": graph.ReturnUsageArgument, // chained receiver feeds .Method()
		"f14": graph.ReturnUsageAssigned, // channel send binds the value
		"f15": graph.ReturnUsagePartiallyIgnored,
		"f16": graph.ReturnUsageAssigned,
		"f17": graph.ReturnUsageAssigned, // composite-literal field, then bound
		"f18": graph.ReturnUsageReturned,
	} {
		assert.Equal(t, want, callReturnUsage(t, result.Edges, callee), "callee %s", callee)
	}
}

// A Go range clause with no binding targets (`for range f()`) consumes
// the result by iterating it but captures nothing — discarded, not
// assigned. The zero-sink fold must not read it as a binding.
func TestGoReturnUsageRangeNoVars(t *testing.T) {
	src := []byte(`package main

func caller() {
	for range f1() {
	}
	for k := range f2() {
		_ = k
	}
}
`)
	e := NewGoExtractor()
	result, err := e.Extract("main.go", src)
	require.NoError(t, err)
	defer result.Tree.Release()

	assert.Equal(t, graph.ReturnUsageDiscarded, callReturnUsage(t, result.Edges, "f1"),
		"for range with no variables captures nothing")
	assert.Equal(t, graph.ReturnUsageAssigned, callReturnUsage(t, result.Edges, "f2"),
		"for k := range binds the induction variable")
}

func TestPythonReturnUsage(t *testing.T) {
	src := []byte(`def caller(self):
    f1()
    x = f2()
    x += f3()
    a, _ = f4()
    _ = f5()
    g(f6())
    if f7():
        pass
    while f8():
        pass
    for item in f9():
        pass
    f10().chained()
    h = lambda: f11()
    self.m1()
    y = self.m2()
    return f12()
`)
	e := NewPythonExtractor()
	result, err := e.Extract("main.py", src)
	require.NoError(t, err)

	for callee, want := range map[string]string{
		"f1":  graph.ReturnUsageDiscarded,
		"f2":  graph.ReturnUsageAssigned,
		"f3":  graph.ReturnUsageAssigned,
		"f4":  graph.ReturnUsagePartiallyIgnored,
		"f5":  graph.ReturnUsageDiscarded,
		"f6":  graph.ReturnUsageArgument,
		"f7":  graph.ReturnUsageCondition,
		"f8":  graph.ReturnUsageCondition,
		"f9":  graph.ReturnUsageCondition,
		"f10": graph.ReturnUsageArgument,
		"f11": graph.ReturnUsageReturned, // lambda body is its return value
		"m1":  graph.ReturnUsageDiscarded,
		"m2":  graph.ReturnUsageAssigned,
		"f12": graph.ReturnUsageReturned,
	} {
		assert.Equal(t, want, callReturnUsage(t, result.Edges, callee), "callee %s", callee)
	}
}

func TestJavaScriptReturnUsage(t *testing.T) {
	src := []byte(`function caller() {
	f1();
	const x = f2();
	y = f3();
	y += f4();
	g(f5());
	if (f6()) { }
	while (f7()) { }
	for (let i = 0; f8(); i++) { }
	for (const a of f9()) { }
	switch (f10()) { }
	f11().chained();
	const h = () => f12();
	new C(f13());
	obj.m1();
	const z = obj.m2();
	return f14();
}
`)
	e := NewJavaScriptExtractor()
	result, err := e.Extract("main.js", src)
	require.NoError(t, err)

	for callee, want := range map[string]string{
		"f1":  graph.ReturnUsageDiscarded,
		"f2":  graph.ReturnUsageAssigned,
		"f3":  graph.ReturnUsageAssigned,
		"f4":  graph.ReturnUsageAssigned,
		"f5":  graph.ReturnUsageArgument,
		"f6":  graph.ReturnUsageCondition,
		"f7":  graph.ReturnUsageCondition,
		"f8":  graph.ReturnUsageCondition,
		"f9":  graph.ReturnUsageCondition,
		"f10": graph.ReturnUsageCondition,
		"f11": graph.ReturnUsageArgument,
		"f12": graph.ReturnUsageReturned, // concise arrow body is its return value
		"f13": graph.ReturnUsageArgument,
		"m1":  graph.ReturnUsageDiscarded,
		"m2":  graph.ReturnUsageAssigned,
		"f14": graph.ReturnUsageReturned,
	} {
		assert.Equal(t, want, callReturnUsage(t, result.Edges, callee), "callee %s", callee)
	}
}

func TestTypeScriptReturnUsage(t *testing.T) {
	src := []byte(`function caller(): number {
	f1();
	const x = f2() as number;
	const y = f3()!;
	if (f4()) { }
	g(f5());
	svc.m1();
	const z = svc.m2();
	return f6();
}
`)
	e := NewTypeScriptExtractor()
	result, err := e.Extract("main.ts", src)
	require.NoError(t, err)

	for callee, want := range map[string]string{
		"f1": graph.ReturnUsageDiscarded,
		"f2": graph.ReturnUsageAssigned, // through the `as` cast
		"f3": graph.ReturnUsageAssigned, // through the non-null assertion
		"f4": graph.ReturnUsageCondition,
		"f5": graph.ReturnUsageArgument,
		"m1": graph.ReturnUsageDiscarded,
		"m2": graph.ReturnUsageAssigned,
		"f6": graph.ReturnUsageReturned,
	} {
		assert.Equal(t, want, callReturnUsage(t, result.Edges, callee), "callee %s", callee)
	}
}

func TestJavaReturnUsage(t *testing.T) {
	src := []byte(`class A {
	void caller() {
		f1();
		int x = f2();
		x = f3();
		g(f4());
		if (f5() > 0) { }
		while (f6()) { }
		for (int i = 0; f7(); i++) { }
		for (var a : f8()) { }
		switch (f9()) { }
		f10().chained();
		Supplier<Integer> s = () -> f11();
		return f12();
	}
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("A.java", src)
	require.NoError(t, err)

	for callee, want := range map[string]string{
		"f1":  graph.ReturnUsageDiscarded,
		"f2":  graph.ReturnUsageAssigned,
		"f3":  graph.ReturnUsageAssigned,
		"f4":  graph.ReturnUsageArgument,
		"f5":  graph.ReturnUsageCondition,
		"f6":  graph.ReturnUsageCondition,
		"f7":  graph.ReturnUsageCondition,
		"f8":  graph.ReturnUsageCondition,
		"f9":  graph.ReturnUsageCondition,
		"f10": graph.ReturnUsageArgument,
		"f11": graph.ReturnUsageReturned, // expression-bodied lambda
		"f12": graph.ReturnUsageReturned,
	} {
		assert.Equal(t, want, callReturnUsage(t, result.Edges, callee), "callee %s", callee)
	}
}

func TestRustReturnUsage(t *testing.T) {
	src := []byte(`fn caller() -> i32 {
	f1();
	let x = f2();
	let (a, _) = f3();
	let _ = f4();
	x = f5();
	g(f6());
	if f7() > 0 { }
	if let Some(v) = f8() { }
	while f9() { }
	match f10() { _ => {} }
	for i in f11() { }
	f12().chained();
	let c = || f13();
	return f14();
}

fn tail_caller() -> i32 {
	f15()
}

fn branch_tail() -> i32 {
	let x = if true { f16() } else { 0 };
	x
}
`)
	e := NewRustExtractor()
	result, err := e.Extract("main.rs", src)
	require.NoError(t, err)

	for callee, want := range map[string]string{
		"f1":  graph.ReturnUsageDiscarded,
		"f2":  graph.ReturnUsageAssigned,
		"f3":  graph.ReturnUsagePartiallyIgnored,
		"f4":  graph.ReturnUsageDiscarded, // wildcard binding throws the value away
		"f5":  graph.ReturnUsageAssigned,
		"f6":  graph.ReturnUsageArgument,
		"f7":  graph.ReturnUsageCondition,
		"f8":  graph.ReturnUsageCondition, // if-let binds inside the condition
		"f9":  graph.ReturnUsageCondition,
		"f10": graph.ReturnUsageCondition,
		"f11": graph.ReturnUsageCondition,
		"f12": graph.ReturnUsageArgument,
		"f13": graph.ReturnUsageReturned, // closure body is its return value
		"f14": graph.ReturnUsageReturned,
		"f15": graph.ReturnUsageReturned, // implicit tail return
		"f16": graph.ReturnUsageAssigned, // branch value flows into the let
	} {
		assert.Equal(t, want, callReturnUsage(t, result.Edges, callee), "callee %s", callee)
	}
}

func TestRubyReturnUsage(t *testing.T) {
	src := []byte(`def caller
  f1()
  x = f2()
  a, _ = f3()
  x += f4()
  g(f5())
  if f6()
    nil
  end
  while f7()
    nil
  end
  case f8()
  when 1 then nil
  end
  f9().chained
  foo if f10()
  return f11()
end

def tail_caller
  f12()
end
`)
	e := NewRubyExtractor()
	result, err := e.Extract("main.rb", src)
	require.NoError(t, err)

	for callee, want := range map[string]string{
		"f1":  graph.ReturnUsageDiscarded,
		"f2":  graph.ReturnUsageAssigned,
		"f3":  graph.ReturnUsagePartiallyIgnored,
		"f4":  graph.ReturnUsageAssigned,
		"f5":  graph.ReturnUsageArgument,
		"f6":  graph.ReturnUsageCondition,
		"f7":  graph.ReturnUsageCondition,
		"f8":  graph.ReturnUsageCondition,
		"f9":  graph.ReturnUsageArgument,
		"f10": graph.ReturnUsageCondition, // statement modifier
		"f11": graph.ReturnUsageReturned,
		"f12": graph.ReturnUsageReturned, // implicit tail return
	} {
		assert.Equal(t, want, callReturnUsage(t, result.Edges, callee), "callee %s", callee)
	}
}

// A call inside a Ruby block / do_block must be classified relative to
// the block, never to the statement that consumes the block. Crossing
// the closure boundary would mislabel a block-internal call as assigned
// (to the receiver of a `.map` assignment), as a condition (the if the
// block result feeds), or as the enclosing method's return.
func TestRubyReturnUsageBlockBoundary(t *testing.T) {
	src := []byte(`def caller
  items.each do |i|
    f1(i)
  end
  y = items.map { |i| f2(i) }
  if items.any? { |i| f3(i) }
    nil
  end
  return items.map do |i|
    f4(i)
  end
end
`)
	e := NewRubyExtractor()
	result, err := e.Extract("main.rb", src)
	require.NoError(t, err)

	for callee, want := range map[string]string{
		// Each block call is the block's tail value: returned from the
		// block, not inheriting the consuming statement's label.
		"f1": graph.ReturnUsageReturned, // bare `.each` block, not the method body
		"f2": graph.ReturnUsageReturned, // `.map` block, not assigned to y
		"f3": graph.ReturnUsageReturned, // `.any?` block, not the if condition
		"f4": graph.ReturnUsageReturned, // block under `return`, returned from the block
	} {
		assert.Equal(t, want, callReturnUsage(t, result.Edges, callee), "callee %s", callee)
	}
}

// A call that is NOT the tail of a Ruby block body is a bare statement
// inside the block — discarded — and likewise must not leak the
// enclosing statement's label across the closure boundary.
func TestRubyReturnUsageBlockNonTail(t *testing.T) {
	src := []byte(`def caller
  y = items.map do |i|
    f_mid(i)
    g(i)
  end
end
`)
	e := NewRubyExtractor()
	result, err := e.Extract("main.rb", src)
	require.NoError(t, err)

	assert.Equal(t, graph.ReturnUsageDiscarded, callReturnUsage(t, result.Edges, "f_mid"),
		"non-tail block statement is discarded, not assigned to y")
	assert.Equal(t, graph.ReturnUsageReturned, callReturnUsage(t, result.Edges, "g"),
		"block tail is returned from the block, not assigned to y")
}

func TestCSharpReturnUsage(t *testing.T) {
	src := []byte(`class A {
	void Caller() {
		F1();
		int x = F2();
		var y = F3();
		x = F4();
		G(F5());
		if (F6() > 0) { }
		while (F7()) { }
		for (int i = 0; F8(); i++) { }
		foreach (var a in F9()) { }
		switch (F10()) { }
		F11().Chained();
		Func<int> h = () => F12();
		return F13();
	}

	int Shorthand() => F14();
}
`)
	e := NewCSharpExtractor()
	result, err := e.Extract("A.cs", src)
	require.NoError(t, err)

	for callee, want := range map[string]string{
		"F1":  graph.ReturnUsageDiscarded,
		"F2":  graph.ReturnUsageAssigned,
		"F3":  graph.ReturnUsageAssigned,
		"F4":  graph.ReturnUsageAssigned,
		"F5":  graph.ReturnUsageArgument,
		"F6":  graph.ReturnUsageCondition,
		"F7":  graph.ReturnUsageCondition,
		"F8":  graph.ReturnUsageCondition,
		"F9":  graph.ReturnUsageCondition,
		"F10": graph.ReturnUsageCondition,
		"F11": graph.ReturnUsageArgument,
		"F12": graph.ReturnUsageReturned, // expression-bodied lambda
		"F13": graph.ReturnUsageReturned,
		"F14": graph.ReturnUsageReturned, // expression-bodied member
	} {
		assert.Equal(t, want, callReturnUsage(t, result.Edges, callee), "callee %s", callee)
	}
}

// The C# switch_expression carries no `value` field — its governing
// expression is positional, ahead of the `switch` keyword. The
// classifier must still place a call in that position as a condition,
// and must not mistake an arm-body call for the condition.
func TestCSharpReturnUsageSwitchExpression(t *testing.T) {
	src := []byte(`class A {
	int Caller(int n) {
		return F1() switch {
			1 => F2(),
			_ => 0,
		};
	}
}
`)
	e := NewCSharpExtractor()
	result, err := e.Extract("A.cs", src)
	require.NoError(t, err)

	assert.Equal(t, graph.ReturnUsageCondition, callReturnUsage(t, result.Edges, "F1"),
		"the governing expression of a switch expression reads as a condition")
	// F2 sits in an arm body — its value becomes the switch-expression
	// result, a shape the classifier does not model; it must at least
	// never be mislabeled as the condition.
	assert.NotEqual(t, graph.ReturnUsageCondition, callReturnUsage(t, result.Edges, "F2"),
		"an arm-body call is not the switch condition")
}

// A language without go/defer must never see those labels, and an
// unclassifiable parent chain must leave the edge unstamped rather
// than mislabeled.
func TestReturnUsageNeverFabricatesLabels(t *testing.T) {
	src := []byte(`def caller():
    raise f1()
`)
	e := NewPythonExtractor()
	result, err := e.Extract("main.py", src)
	require.NoError(t, err)

	got := callReturnUsage(t, result.Edges, "f1")
	assert.Empty(t, got, "raise is not a covered consumption shape — no label")
	for _, edge := range result.Edges {
		usage := graph.ReturnUsageOf(edge)
		assert.NotEqual(t, graph.ReturnUsageGoroutine, usage)
		assert.NotEqual(t, graph.ReturnUsageDeferred, usage)
	}
}
