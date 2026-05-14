package indexer

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func TestMarkTestSymbolsAndEmitEdges_GoStyle(t *testing.T) {
	g := graph.New()

	// Two files: one prod, one test.
	g.AddNode(&graph.Node{ID: "pkg/foo.go", Kind: graph.KindFile, Name: "pkg/foo.go", FilePath: "pkg/foo.go", Language: "go"})
	g.AddNode(&graph.Node{ID: "pkg/foo_test.go", Kind: graph.KindFile, Name: "pkg/foo_test.go", FilePath: "pkg/foo_test.go", Language: "go"})

	// Subject under test plus a helper, both in pkg/foo.go.
	g.AddNode(&graph.Node{ID: "pkg/foo.go::Foo", Kind: graph.KindFunction, Name: "Foo", FilePath: "pkg/foo.go", Language: "go"})
	g.AddNode(&graph.Node{ID: "pkg/foo.go::helper", Kind: graph.KindFunction, Name: "helper", FilePath: "pkg/foo.go", Language: "go"})

	// Test function in pkg/foo_test.go.
	g.AddNode(&graph.Node{ID: "pkg/foo_test.go::TestFoo", Kind: graph.KindFunction, Name: "TestFoo", FilePath: "pkg/foo_test.go", Language: "go"})

	// Calls.
	g.AddEdge(&graph.Edge{From: "pkg/foo_test.go::TestFoo", To: "pkg/foo.go::Foo", Kind: graph.EdgeCalls, FilePath: "pkg/foo_test.go", Line: 10})
	g.AddEdge(&graph.Edge{From: "pkg/foo.go::Foo", To: "pkg/foo.go::helper", Kind: graph.EdgeCalls, FilePath: "pkg/foo.go", Line: 5})

	marked, emitted := markTestSymbolsAndEmitEdges(g)

	if marked != 1 {
		t.Fatalf("expected 1 test symbol marked, got %d", marked)
	}
	if emitted != 1 {
		t.Fatalf("expected 1 EdgeTests, got %d", emitted)
	}

	// TestFoo must be flagged.
	testFn := g.GetNode("pkg/foo_test.go::TestFoo")
	if isTest, _ := testFn.Meta["is_test"].(bool); !isTest {
		t.Fatalf("TestFoo should be flagged is_test, got %v", testFn.Meta["is_test"])
	}

	// EdgeTests must point from TestFoo → Foo.
	var found bool
	for _, e := range g.AllEdges() {
		if e.Kind == graph.EdgeTests && e.From == "pkg/foo_test.go::TestFoo" && e.To == "pkg/foo.go::Foo" {
			found = true
			if e.Line != 10 {
				t.Errorf("EdgeTests line = %d, want 10", e.Line)
			}
		}
	}
	if !found {
		t.Fatalf("EdgeTests TestFoo→Foo not found")
	}

	// Foo → helper is a prod-to-prod call; no EdgeTests should be emitted.
	for _, e := range g.AllEdges() {
		if e.Kind == graph.EdgeTests && e.From == "pkg/foo.go::Foo" {
			t.Fatalf("unexpected EdgeTests from prod fn: %+v", e)
		}
	}
}

func TestMarkTestSymbolsAndEmitEdges_NameAloneIsNotEnough(t *testing.T) {
	g := graph.New()

	// A production file holding a function whose name happens to match
	// the Go test prefix — e.g. TestRole in testpattern.go. go test
	// never picks this up, so it must not be flagged.
	g.AddNode(&graph.Node{ID: "pkg/p.go", Kind: graph.KindFile, Name: "pkg/p.go", FilePath: "pkg/p.go", Language: "go"})
	g.AddNode(&graph.Node{ID: "pkg/p.go::TestRole", Kind: graph.KindFunction, Name: "TestRole", FilePath: "pkg/p.go", Language: "go"})
	g.AddNode(&graph.Node{ID: "pkg/p.go::BenchmarkConfig", Kind: graph.KindFunction, Name: "BenchmarkConfig", FilePath: "pkg/p.go", Language: "go"})

	marked, _ := markTestSymbolsAndEmitEdges(g)
	if marked != 0 {
		t.Fatalf("expected 0 test symbols marked for prod file, got %d", marked)
	}
	for _, id := range []string{"pkg/p.go::TestRole", "pkg/p.go::BenchmarkConfig"} {
		n := g.GetNode(id)
		if v, _ := n.Meta["is_test"].(bool); v {
			t.Fatalf("%s in a non-test file must not be flagged is_test", id)
		}
		if _, ok := n.Meta["test_role"]; ok {
			t.Fatalf("%s in a non-test file must not carry test_role", id)
		}
	}
}

func TestMarkTestSymbolsAndEmitEdges_RoleRefinement(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{ID: "x_test.go", Kind: graph.KindFile, Name: "x_test.go", FilePath: "x_test.go", Language: "go"})
	g.AddNode(&graph.Node{ID: "x_test.go::TestA", Kind: graph.KindFunction, Name: "TestA", FilePath: "x_test.go", Language: "go"})
	g.AddNode(&graph.Node{ID: "x_test.go::BenchmarkA", Kind: graph.KindFunction, Name: "BenchmarkA", FilePath: "x_test.go", Language: "go"})
	g.AddNode(&graph.Node{ID: "x_test.go::FuzzA", Kind: graph.KindFunction, Name: "FuzzA", FilePath: "x_test.go", Language: "go"})
	g.AddNode(&graph.Node{ID: "x_test.go::ExampleA", Kind: graph.KindFunction, Name: "ExampleA", FilePath: "x_test.go", Language: "go"})
	// Support code in the test file with no recognised prefix → "test".
	g.AddNode(&graph.Node{ID: "x_test.go::setup", Kind: graph.KindFunction, Name: "setup", FilePath: "x_test.go", Language: "go"})

	markTestSymbolsAndEmitEdges(g)

	want := map[string]string{
		"x_test.go::TestA":      "test",
		"x_test.go::BenchmarkA": "benchmark",
		"x_test.go::FuzzA":      "fuzz",
		"x_test.go::ExampleA":   "example",
		"x_test.go::setup":      "test",
	}
	for id, role := range want {
		got, _ := g.GetNode(id).Meta["test_role"].(string)
		if got != role {
			t.Errorf("%s: test_role = %q, want %q", id, got, role)
		}
	}
}

func TestMarkTestSymbolsAndEmitEdges_PythonStyle(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{ID: "app/svc.py", Kind: graph.KindFile, Name: "app/svc.py", FilePath: "app/svc.py", Language: "python"})
	g.AddNode(&graph.Node{ID: "tests/test_svc.py", Kind: graph.KindFile, Name: "tests/test_svc.py", FilePath: "tests/test_svc.py", Language: "python"})

	g.AddNode(&graph.Node{ID: "app/svc.py::greet", Kind: graph.KindFunction, Name: "greet", FilePath: "app/svc.py", Language: "python"})
	g.AddNode(&graph.Node{ID: "tests/test_svc.py::test_greet", Kind: graph.KindFunction, Name: "test_greet", FilePath: "tests/test_svc.py", Language: "python"})

	g.AddEdge(&graph.Edge{From: "tests/test_svc.py::test_greet", To: "app/svc.py::greet", Kind: graph.EdgeCalls, FilePath: "tests/test_svc.py", Line: 3})

	_, emitted := markTestSymbolsAndEmitEdges(g)
	if emitted != 1 {
		t.Fatalf("expected 1 EdgeTests, got %d", emitted)
	}
}

func TestMarkTestSymbolsAndEmitEdges_DropsTestToTestCalls(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{ID: "x_test.go", Kind: graph.KindFile, Name: "x_test.go", FilePath: "x_test.go", Language: "go"})

	g.AddNode(&graph.Node{ID: "x_test.go::TestA", Kind: graph.KindFunction, Name: "TestA", FilePath: "x_test.go", Language: "go"})
	g.AddNode(&graph.Node{ID: "x_test.go::setupFixture", Kind: graph.KindFunction, Name: "setupFixture", FilePath: "x_test.go", Language: "go"})

	// Test calls a helper in the same test file — no EdgeTests should emit.
	g.AddEdge(&graph.Edge{From: "x_test.go::TestA", To: "x_test.go::setupFixture", Kind: graph.EdgeCalls, FilePath: "x_test.go", Line: 4})

	_, emitted := markTestSymbolsAndEmitEdges(g)
	if emitted != 0 {
		t.Fatalf("expected 0 EdgeTests for test→test call, got %d", emitted)
	}
}

func TestMarkTestSymbolsAndEmitEdges_DedupesParallelCalls(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{ID: "x_test.go", Kind: graph.KindFile, Name: "x_test.go", FilePath: "x_test.go", Language: "go"})
	g.AddNode(&graph.Node{ID: "x.go", Kind: graph.KindFile, Name: "x.go", FilePath: "x.go", Language: "go"})
	g.AddNode(&graph.Node{ID: "x_test.go::TestA", Kind: graph.KindFunction, Name: "TestA", FilePath: "x_test.go", Language: "go"})
	g.AddNode(&graph.Node{ID: "x.go::F", Kind: graph.KindFunction, Name: "F", FilePath: "x.go", Language: "go"})

	// Same call twice on different lines (the test calls F at line 5 and 12).
	g.AddEdge(&graph.Edge{From: "x_test.go::TestA", To: "x.go::F", Kind: graph.EdgeCalls, FilePath: "x_test.go", Line: 5})
	g.AddEdge(&graph.Edge{From: "x_test.go::TestA", To: "x.go::F", Kind: graph.EdgeCalls, FilePath: "x_test.go", Line: 12})

	_, emitted := markTestSymbolsAndEmitEdges(g)
	if emitted != 1 {
		t.Fatalf("expected 1 deduped EdgeTests, got %d", emitted)
	}
}
