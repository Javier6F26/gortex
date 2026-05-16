package languages

import (
	"strings"
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func runScalaExtract(t *testing.T, path, src string) ([]*graph.Node, []*graph.Edge) {
	t.Helper()
	ext := NewScalaExtractor()
	result, err := ext.Extract(path, []byte(src))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	return result.Nodes, result.Edges
}

// scalaAnnotationTargets returns the (from, to) pairs of EdgeAnnotated
// edges, keyed by from-symbol-ID, so tests can assert "this declaration
// is annotated with these names".
func scalaAnnotationTargets(edges []*graph.Edge) map[string][]string {
	out := map[string][]string{}
	for _, e := range edges {
		if e.Kind != graph.EdgeAnnotated {
			continue
		}
		out[e.From] = append(out[e.From], e.To)
	}
	return out
}

func TestScalaAnnotations_ClassWithMultiple(t *testing.T) {
	src := `@deprecated("use newF", "1.5")
@inline
class Foo {
  def bar(n: Int): Int = n
}
`
	_, edges := runScalaExtract(t, "src/Foo.scala", src)
	pairs := scalaAnnotationTargets(edges)
	got := pairs["src/Foo.scala::Foo"]
	if len(got) != 2 {
		t.Fatalf("expected 2 annotations on Foo, got %d: %v", len(got), got)
	}
	wantSet := map[string]bool{
		"annotation::scala::deprecated": true,
		"annotation::scala::inline":     true,
	}
	for _, to := range got {
		if !wantSet[to] {
			t.Errorf("unexpected annotation target: %s", to)
		}
	}
}

func TestScalaAnnotations_PreservesArgs(t *testing.T) {
	src := `@deprecated("use newF", "1.5")
class Foo
`
	_, edges := runScalaExtract(t, "src/Foo.scala", src)
	for _, e := range edges {
		if e.Kind != graph.EdgeAnnotated {
			continue
		}
		if e.To != "annotation::scala::deprecated" {
			continue
		}
		args, _ := e.Meta["args"].(string)
		if !strings.Contains(args, "use newF") {
			t.Errorf("args should preserve verbatim text, got %q", args)
		}
		return
	}
	t.Fatal("no EdgeAnnotated emitted for @deprecated")
}

func TestScalaAnnotations_MethodInsideClass(t *testing.T) {
	src := `class Foo {
  @tailrec
  def bar(n: Int): Int = if (n == 0) 0 else bar(n - 1)
}
`
	_, edges := runScalaExtract(t, "src/Foo.scala", src)
	pairs := scalaAnnotationTargets(edges)
	got := pairs["src/Foo.scala::Foo.bar"]
	if len(got) != 1 || got[0] != "annotation::scala::tailrec" {
		t.Errorf("method bar should have @tailrec annotation, got %v", got)
	}
	// The class itself must NOT inherit the method's annotation.
	classGot := pairs["src/Foo.scala::Foo"]
	for _, to := range classGot {
		if to == "annotation::scala::tailrec" {
			t.Errorf("class Foo should NOT see method's @tailrec annotation")
		}
	}
}

func TestScalaAnnotations_TopLevelFunction(t *testing.T) {
	src := `@main def app(): Unit = println("hi")
`
	_, edges := runScalaExtract(t, "src/Main.scala", src)
	pairs := scalaAnnotationTargets(edges)
	got := pairs["src/Main.scala::app"]
	if len(got) != 1 || got[0] != "annotation::scala::main" {
		t.Errorf("top-level app should have @main annotation, got %v", got)
	}
}

func TestScalaAnnotations_TraitWithAnnotation(t *testing.T) {
	src := `@deprecated("use NewTrait", "2.0")
trait OldTrait {
  def foo(): Int
}
`
	_, edges := runScalaExtract(t, "src/Old.scala", src)
	pairs := scalaAnnotationTargets(edges)
	got := pairs["src/Old.scala::OldTrait"]
	if len(got) != 1 || got[0] != "annotation::scala::deprecated" {
		t.Errorf("trait OldTrait should have @deprecated, got %v", got)
	}
}

func TestScalaAnnotations_ObjectWithAnnotation(t *testing.T) {
	src := `@SerialVersionUID(1L)
object MyObj {
  def thing(): Int = 1
}
`
	_, edges := runScalaExtract(t, "src/Obj.scala", src)
	pairs := scalaAnnotationTargets(edges)
	got := pairs["src/Obj.scala::MyObj"]
	if len(got) != 1 || got[0] != "annotation::scala::SerialVersionUID" {
		t.Errorf("object MyObj should have @SerialVersionUID, got %v", got)
	}
}

func TestScalaAnnotations_SyntheticNodeDedupedAcrossUses(t *testing.T) {
	src := `@inline class A
@inline class B
@inline class C
`
	nodes, _ := runScalaExtract(t, "src/many.scala", src)
	count := 0
	for _, n := range nodes {
		if n.ID == "annotation::scala::inline" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected single synthetic annotation node, got %d copies", count)
	}
}

func TestScalaAnnotations_NoAnnotationsLeavesGraphCleanly(t *testing.T) {
	src := `class Plain {
  def x(): Int = 1
}
`
	_, edges := runScalaExtract(t, "src/Plain.scala", src)
	for _, e := range edges {
		if e.Kind == graph.EdgeAnnotated {
			t.Errorf("unannotated class should NOT emit EdgeAnnotated, got: %+v", e)
		}
	}
}
