package tstypes

import (
	"testing"

	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/graph"
)

const pySvc = `class Svc:
    def run(self):
        pass

    def stop(self):
        pass
`

func TestPython_AnnotatedParamResolvesCall(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app/svc.py": pySvc,
		"app/main.py": `from app.svc import Svc


def handle(s: Svc):
    s.run()
`,
	})
	p := NewProvider(PythonSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "handle", graph.KindFunction)
	target := nodeByNameKind(t, g, "run", graph.KindMethod)
	e := callEdgeTo(g, caller.ID, target.ID)
	if e == nil {
		t.Fatalf("annotated-param call not resolved; edges: %v", g.GetOutEdges(caller.ID))
	}
	assertASTProvenance(t, e, "python-types")
}

// `s = Svc()` is convention-based constructor inference — it must only
// fire because Svc resolves to a real class node.
func TestPython_ConstructorInferenceResolvesCall(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app/svc.py": pySvc,
		"app/main.py": `from app.svc import Svc


def main():
    s = Svc()
    s.run()
`,
	})
	p := NewProvider(PythonSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "main", graph.KindFunction)
	target := nodeByNameKind(t, g, "run", graph.KindMethod)
	if callEdgeTo(g, caller.ID, target.ID) == nil {
		t.Fatalf("constructor-inferred call not resolved; edges: %v", g.GetOutEdges(caller.ID))
	}
}

// A capitalized factory that is NOT a class must not ground a receiver.
func TestPython_CapitalizedFactoryDoesNotGround(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app/util.py": `def Build():
    return object()
`,
		"app/main.py": `from app.util import Build


def main():
    s = Build()
    s.run()
`,
	})
	p := NewProvider(PythonSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "main", graph.KindFunction)
	assertUntouched(t, g, caller.ID, "run", "python-types")
}

func TestPython_ImportHintDisambiguates(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app/svc.py":   pySvc,
		"other/svc.py": pySvc,
		"app/main.py": `from app.svc import Svc


def main():
    s = Svc()
    s.run()
`,
	})
	p := NewProvider(PythonSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "main", graph.KindFunction)
	want := "app/svc.py::Svc.run"
	if callEdgeTo(g, caller.ID, want) == nil {
		t.Fatalf("import-hinted call did not land on %s; edges: %v", want, g.GetOutEdges(caller.ID))
	}
	if callEdgeTo(g, caller.ID, "other/svc.py::Svc.run") != nil {
		t.Fatal("call landed on the wrong module's class")
	}
}

func TestPython_BaseClassExtendsSynthesis(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app/svc.py": pySvc,
		"app/sub.py": `from app.svc import Svc


class Sub(Svc):
    def extra(self):
        pass
`,
	})
	p := NewProvider(PythonSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	sub := nodeByNameKind(t, g, "Sub", graph.KindType)
	svc := nodeByNameKind(t, g, "Svc", graph.KindType)
	e := edgeBetween(g, sub.ID, graph.EdgeExtends, svc.ID)
	if e == nil {
		t.Fatalf("extends edge missing; edges: %v", g.GetOutEdges(sub.ID))
	}
	assertASTProvenance(t, e, "python-types")
}

// self-qualified calls and `self.x = Svc()` fields resolve in-class.
func TestPython_SelfAndFieldReceivers(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app/svc.py": pySvc,
		"app/app.py": `from app.svc import Svc


class App:
    def __init__(self):
        self.worker = Svc()

    def direct(self):
        self.helper()

    def helper(self):
        self.worker.run()
`,
	})
	p := NewProvider(PythonSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	direct := nodeByNameKind(t, g, "direct", graph.KindMethod)
	helper := nodeByNameKind(t, g, "helper", graph.KindMethod)
	if callEdgeTo(g, direct.ID, helper.ID) == nil {
		t.Fatalf("self.helper() not resolved; edges: %v", g.GetOutEdges(direct.ID))
	}
	run := nodeByNameKind(t, g, "run", graph.KindMethod)
	if callEdgeTo(g, helper.ID, run.ID) == nil {
		t.Fatalf("self.worker.run() not resolved; edges: %v", g.GetOutEdges(helper.ID))
	}
}

func TestPython_AmbiguousReceiverStaysUntouched(t *testing.T) {
	g, dir := buildFixture(t, map[string]string{
		"app/svc.py": pySvc,
		"app/alt.py": `class Alt:
    def run(self):
        pass
`,
		"app/main.py": `from app.alt import Alt
from app.svc import Svc


def main():
    s = Svc()
    s = Alt()
    s.run()
`,
	})
	p := NewProvider(PythonSpec(), zap.NewNop())
	if _, err := p.Enrich(g, dir); err != nil {
		t.Fatal(err)
	}
	caller := nodeByNameKind(t, g, "main", graph.KindFunction)
	assertUntouched(t, g, caller.ID, "run", "python-types")
}
