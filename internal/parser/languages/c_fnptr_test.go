package languages

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func fnPtrReg(edges []*graph.Edge, st, field string) *graph.Edge {
	for _, e := range edges {
		if e.Kind != graph.EdgeReferences || e.Meta == nil {
			continue
		}
		if v, _ := e.Meta["via"].(string); v != "fn-pointer-reg" {
			continue
		}
		s, _ := e.Meta["fnptr_struct"].(string)
		f, _ := e.Meta["fnptr_field"].(string)
		if s == st && f == field {
			return e
		}
	}
	return nil
}

func fnPtrRegFn(edges []*graph.Edge, fn string) *graph.Edge {
	for _, e := range edges {
		if e.Meta == nil {
			continue
		}
		if v, _ := e.Meta["via"].(string); v != "fn-pointer-reg" {
			continue
		}
		if f, _ := e.Meta["fnptr_fn"].(string); f == fn {
			return e
		}
	}
	return nil
}

func fnPtrDisp(edges []*graph.Edge, st, field string) *graph.Edge {
	for _, e := range edges {
		if e.Kind != graph.EdgeCalls || e.Meta == nil {
			continue
		}
		if v, _ := e.Meta["via"].(string); v != "fn-pointer-dispatch" {
			continue
		}
		s, _ := e.Meta["fnptr_struct"].(string)
		f, _ := e.Meta["fnptr_field"].(string)
		if s == st && f == field {
			return e
		}
	}
	return nil
}

func TestCFnPtr_CommandTablePattern(t *testing.T) {
	src := `typedef int (*cmd_fn)(int, char**);
struct cmd { char *name; int (*fn)(int,char**); };
int cmd_add(int a, char**b){return 0;}
int cmd_rm(int a, char**b){return 0;}
static struct cmd cmds[] = { {"add", cmd_add}, {"rm", cmd_rm} };
int run(int i, int argc, char **argv) {
  return cmds[i].fn(argc, argv);
}
`
	res, err := NewCExtractor().Extract("cmds.c", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if fnPtrRegFn(res.Edges, "cmd_add") == nil || fnPtrRegFn(res.Edges, "cmd_rm") == nil {
		t.Errorf("positional registrations for cmd_add/cmd_rm not captured")
	}
	d := fnPtrDisp(res.Edges, "cmd", "fn")
	if d == nil {
		t.Fatalf("dispatch placeholder for cmds[i].fn() not stamped")
	}
	if d.From != "cmds.c::run" {
		t.Errorf("dispatch From = %q (want cmds.c::run)", d.From)
	}
}

func TestCFnPtr_DesignatedAndTypedefField(t *testing.T) {
	src := `typedef int (*op_fn)(void);
struct ops { op_fn handler; };
int op_x(void){return 0;}
struct ops base = { .handler = op_x };
int run_op(struct ops *o) { return o->handler(); }
`
	res, err := NewCExtractor().Extract("ops.c", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if r := fnPtrReg(res.Edges, "ops", "handler"); r == nil {
		t.Errorf("designated registration .handler = op_x not captured")
	}
	if fnPtrDisp(res.Edges, "ops", "handler") == nil {
		t.Errorf("dispatch o->handler() not stamped (typedef'd fn-pointer field)")
	}
}

func TestCFnPtr_FieldCopyCaptured(t *testing.T) {
	src := `typedef int (*h_fn)(void);
struct a { h_fn h; };
struct b { h_fn h; };
void wire(struct a *x, struct b *y) { x->h = y->h; }
`
	res, err := NewCExtractor().Extract("wire.c", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	var copyEdge *graph.Edge
	for _, e := range res.Edges {
		if e.Meta != nil {
			if cf, _ := e.Meta["fnptr_copy_field"].(string); cf == "h" {
				copyEdge = e
			}
		}
	}
	if copyEdge == nil {
		t.Fatalf("field-copy x->h = y->h not captured")
	}
	if st, _ := copyEdge.Meta["fnptr_struct"].(string); st != "a" {
		t.Errorf("copy target struct = %q (want a)", st)
	}
	if cs, _ := copyEdge.Meta["fnptr_copy_struct"].(string); cs != "b" {
		t.Errorf("copy source struct = %q (want b)", cs)
	}
}

func TestCppFnPtr_StructDispatch(t *testing.T) {
	// The shared pass also runs for C++ structs.
	src := `struct Ops { int (*run)(int); };
int impl(int x){return x;}
static struct Ops table = { impl };
int dispatch(struct Ops *o) { return o->run(1); }
`
	res, err := NewCppExtractor().Extract("ops.cpp", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if fnPtrRegFn(res.Edges, "impl") == nil {
		t.Errorf("C++ struct fn-pointer registration not captured")
	}
	if fnPtrDisp(res.Edges, "Ops", "run") == nil {
		t.Errorf("C++ dispatch o->run() not stamped")
	}
}
