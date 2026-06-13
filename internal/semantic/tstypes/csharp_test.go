package tstypes

import (
	"testing"

	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/graph"
)

const csSvc = `namespace A {
    public class Svc {
        public void Run() {}
        public void Stop() {}
    }
}
`

// `using` directives bind namespaces, not names, so the cross-file
// case rides on repo-unique name resolution.
func TestCSharp_DeclaredParamTypeResolvesCall(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"A/Svc.cs": csSvc,
		"B/App.cs": `namespace B {
    public class App {
        public void Handle(Svc s) {
            s.Run();
        }
    }
}
`,
	})
	p := NewProvider(CSharpSpec(), zap.NewNop())
	res, err := p.Enrich(g, dir)
	if err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "Handle", graph.KindMethod)
	target := nodeByNameKind(t, g, "Run", graph.KindMethod)
	e := callEdgeTo(g, caller.ID, target.ID)
	if e == nil {
		t.Fatalf("annotated-param call not resolved; edges: %v", g.GetOutEdges(caller.ID))
	}
	assertASTProvenance(t, e, "csharp-types")
	if res.EdgesConfirmed+res.EdgesAdded == 0 {
		t.Errorf("result reported no edge work: %+v", res)
	}
}

// Both `Svc s = new Svc()` and `var s = new Svc()` ground the receiver.
func TestCSharp_ConstructorInferenceResolvesCall(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"A/Svc.cs": csSvc,
		"B/App.cs": `namespace B {
    public class App {
        public void Declared() {
            Svc s = new Svc();
            s.Run();
        }

        public void Inferred() {
            var s = new Svc();
            s.Stop();
        }
    }
}
`,
	})
	p := NewProvider(CSharpSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	declared := nodeByNameKind(t, g, "Declared", graph.KindMethod)
	run := nodeByNameKind(t, g, "Run", graph.KindMethod)
	if callEdgeTo(g, declared.ID, run.ID) == nil {
		t.Fatalf("declared-type call not resolved; edges: %v", g.GetOutEdges(declared.ID))
	}
	inferred := nodeByNameKind(t, g, "Inferred", graph.KindMethod)
	stop := nodeByNameKind(t, g, "Stop", graph.KindMethod)
	if callEdgeTo(g, inferred.ID, stop.ID) == nil {
		t.Fatalf("var-inferred call not resolved; edges: %v", g.GetOutEdges(inferred.ID))
	}
}

// The base list does not syntactically split the base class from
// interfaces — the resolved node's kind must discriminate.
func TestCSharp_BaseListImplementsAndExtends(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"A/Svc.cs": csSvc,
		"A/IGreeter.cs": `namespace A {
    public interface IGreeter {
        void Greet();
    }
}
`,
		"B/Impl.cs": `namespace B {
    public class Impl : Svc, IGreeter {
        public void Greet() {}
    }
}
`,
	})
	p := NewProvider(CSharpSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	impl := nodeByNameKind(t, g, "Impl", graph.KindType)
	svc := nodeByNameKind(t, g, "Svc", graph.KindType)
	iface := nodeByNameKind(t, g, "IGreeter", graph.KindInterface)

	ee := edgeBetween(g, impl.ID, graph.EdgeExtends, svc.ID)
	if ee == nil {
		t.Fatalf("extends edge missing; edges: %v", g.GetOutEdges(impl.ID))
	}
	assertASTProvenance(t, ee, "csharp-types")

	ie := edgeBetween(g, impl.ID, graph.EdgeImplements, iface.ID)
	if ie == nil {
		t.Fatalf("implements edge missing; edges: %v", g.GetOutEdges(impl.ID))
	}
	assertASTProvenance(t, ie, "csharp-types")
}

// this-qualified calls, typed fields, and typed auto-properties all
// resolve in-class.
func TestCSharp_ThisFieldAndPropertyReceivers(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"A/Svc.cs": csSvc,
		"B/App.cs": `namespace B {
    public class App {
        private Svc worker;
        public Svc Backup { get; set; }

        public void Direct() {
            this.Helper();
        }

        public void Helper() {
            this.worker.Run();
            this.Backup.Stop();
        }
    }
}
`,
	})
	p := NewProvider(CSharpSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	direct := nodeByNameKind(t, g, "Direct", graph.KindMethod)
	helper := nodeByNameKind(t, g, "Helper", graph.KindMethod)
	if callEdgeTo(g, direct.ID, helper.ID) == nil {
		t.Fatalf("this.Helper() not resolved; edges: %v", g.GetOutEdges(direct.ID))
	}
	run := nodeByNameKind(t, g, "Run", graph.KindMethod)
	if callEdgeTo(g, helper.ID, run.ID) == nil {
		t.Fatalf("this.worker.Run() not resolved through field type; edges: %v", g.GetOutEdges(helper.ID))
	}
	stop := nodeByNameKind(t, g, "Stop", graph.KindMethod)
	if callEdgeTo(g, helper.ID, stop.ID) == nil {
		t.Fatalf("this.Backup.Stop() not resolved through property type; edges: %v", g.GetOutEdges(helper.ID))
	}
}

func TestCSharp_AmbiguousReceiverStaysUntouched(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"A/Svc.cs": csSvc,
		"A/Alt.cs": `namespace A {
    public class Alt {
        public void Run() {}
    }
}
`,
		"B/App.cs": `namespace B {
    public class App {
        public void Main() {
            object s;
            s = new Svc();
            s = new Alt();
            s.Run();
        }
    }
}
`,
	})
	p := NewProvider(CSharpSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "Main", graph.KindMethod)
	assertUntouched(t, g, caller.ID, "Run", "csharp-types")
}
