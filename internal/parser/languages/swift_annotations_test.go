package languages

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func runSwiftExtract(t *testing.T, path, src string) ([]*graph.Node, []*graph.Edge) {
	t.Helper()
	ext := NewSwiftExtractor()
	result, err := ext.Extract(path, []byte(src))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	return result.Nodes, result.Edges
}

func swiftAnnotationTargets(edges []*graph.Edge) map[string][]string {
	out := map[string][]string{}
	for _, e := range edges {
		if e.Kind != graph.EdgeAnnotated {
			continue
		}
		out[e.From] = append(out[e.From], e.To)
	}
	return out
}

func TestSwiftAnnotations_ClassObjc(t *testing.T) {
	src := `import Foundation
@objc
class MyViewController: NSObject {
}
`
	_, edges := runSwiftExtract(t, "src/Vc.swift", src)
	pairs := swiftAnnotationTargets(edges)
	got := pairs["src/Vc.swift::MyViewController"]
	if len(got) != 1 || got[0] != "annotation::swift::objc" {
		t.Errorf("expected @objc on MyViewController, got %v", got)
	}
}

func TestSwiftAnnotations_MultipleOnClass(t *testing.T) {
	src := `import Foundation
@objc
@objcMembers
class Wrapper: NSObject {
}
`
	_, edges := runSwiftExtract(t, "src/W.swift", src)
	pairs := swiftAnnotationTargets(edges)
	got := pairs["src/W.swift::Wrapper"]
	if len(got) != 2 {
		t.Fatalf("expected 2 annotations on Wrapper, got %d: %v", len(got), got)
	}
	set := map[string]bool{}
	for _, to := range got {
		set[to] = true
	}
	if !set["annotation::swift::objc"] || !set["annotation::swift::objcMembers"] {
		t.Errorf("missing expected annotations: %v", got)
	}
}

func TestSwiftAnnotations_FunctionAttribute(t *testing.T) {
	src := `@available(iOS 13.0, *)
func render() {
}
`
	_, edges := runSwiftExtract(t, "src/r.swift", src)
	pairs := swiftAnnotationTargets(edges)
	got := pairs["src/r.swift::render"]
	if len(got) != 1 || got[0] != "annotation::swift::available" {
		t.Errorf("expected @available on render, got %v", got)
	}
}

func TestSwiftAnnotations_MethodInsideClass(t *testing.T) {
	src := `import Foundation
class Vm: NSObject {
  @MainActor
  func update() async {
  }
}
`
	_, edges := runSwiftExtract(t, "src/Vm.swift", src)
	pairs := swiftAnnotationTargets(edges)
	got := pairs["src/Vm.swift::Vm.update"]
	if len(got) != 1 || got[0] != "annotation::swift::MainActor" {
		t.Errorf("expected @MainActor on Vm.update, got %v", got)
	}
	// Class itself must NOT inherit method's annotation.
	classGot := pairs["src/Vm.swift::Vm"]
	for _, to := range classGot {
		if to == "annotation::swift::MainActor" {
			t.Errorf("class Vm should NOT see method's @MainActor")
		}
	}
}

func TestSwiftAnnotations_SyntheticNodeDedupedAcrossUses(t *testing.T) {
	src := `import Foundation
@objc class A: NSObject {}
@objc class B: NSObject {}
@objc class C: NSObject {}
`
	nodes, _ := runSwiftExtract(t, "src/many.swift", src)
	count := 0
	for _, n := range nodes {
		if n.ID == "annotation::swift::objc" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected single synthetic annotation node, got %d", count)
	}
}

func TestSwiftAnnotations_NoModifiers_NoEdges(t *testing.T) {
	src := `import Foundation
class Plain: NSObject {
  func x() {
  }
}
`
	_, edges := runSwiftExtract(t, "src/Plain.swift", src)
	for _, e := range edges {
		if e.Kind == graph.EdgeAnnotated {
			t.Errorf("plain class/method must not emit EdgeAnnotated: %+v", e)
		}
	}
}

func TestSwift_ObjCMembersPropagation(t *testing.T) {
	src := `@objcMembers
class C {
    func a() {}
    @nonobjc func b() {}
    func move(from x: Int, to y: Int) {}
    var title: String = ""
    @nonobjc var secret: String = ""
}
`
	nodes, _ := runSwiftExtract(t, "C.swift", src)
	sel := map[string]any{}
	for _, n := range nodes {
		if n.Meta == nil {
			continue
		}
		if s, ok := n.Meta["objc_selector"]; ok {
			sel[n.Name] = s
		}
	}
	if sel["a"] != "a" {
		t.Errorf("@objcMembers should expose a with selector a, got %v", sel["a"])
	}
	if sel["move"] != "moveFrom:to:" {
		t.Errorf("@objcMembers should derive move(from:to:) selector moveFrom:to:, got %v", sel["move"])
	}
	if sel["title"] != "title" {
		t.Errorf("@objcMembers should expose property title, got %v", sel["title"])
	}
	if _, ok := sel["b"]; ok {
		t.Errorf("@nonobjc func b must not be exposed, got selector %v", sel["b"])
	}
	if _, ok := sel["secret"]; ok {
		t.Errorf("@nonobjc var secret must not be exposed, got selector %v", sel["secret"])
	}
}

func TestSwift_ObjCProtocolFlag(t *testing.T) {
	src := `@objc protocol Drawable {
    func draw()
}
protocol Plain {
    func x()
}
`
	nodes, _ := runSwiftExtract(t, "P.swift", src)
	flag := map[string]any{}
	for _, n := range nodes {
		if n.Kind == graph.KindInterface {
			if n.Meta != nil {
				flag[n.Name] = n.Meta["objc"]
			} else {
				flag[n.Name] = nil
			}
		}
	}
	if flag["Drawable"] != true {
		t.Errorf("@objc protocol Drawable should carry Meta[objc]=true, got %v", flag["Drawable"])
	}
	if flag["Plain"] == true {
		t.Errorf("plain protocol Plain must not carry Meta[objc]=true")
	}
}
