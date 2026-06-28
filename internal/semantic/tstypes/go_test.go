package tstypes

import (
	"testing"

	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/semantic"
)

const goProviderName = "go-ast-types"

// runGoSpecDirect runs the GoSpec engine over a fixture graph WITHOUT the
// provider's toolchain-suppression gate. The gate would no-op the provider
// whenever the `go` toolchain is on PATH (always true under `go test`), so
// the spec's resolution logic is exercised by driving the binder + applier
// directly — the same work EnrichRepo does, minus the gate and the
// resolve-mutex a single-goroutine test does not need.
func runGoSpecDirect(t *testing.T, g *graph.Graph, dir string) *semantic.EnrichResult {
	t.Helper()
	spec := GoSpec()
	files := languageFiles(g, spec, "", dir)
	var all []*fileFacts
	for _, ref := range files {
		facts, err := analyzeFile(spec, ref)
		if err != nil {
			t.Fatalf("analyze %s: %v", ref.absPath, err)
		}
		if facts != nil {
			all = append(all, facts)
		}
	}
	res := &semantic.EnrichResult{Provider: spec.ProviderName, Language: spec.Languages[0]}
	ap := newApplier(g, spec, spec.ProviderName)
	ap.applyAll(all, res)
	ap.flush()
	return res
}

// TestGo_ConstructorInferenceResolvesCall is the headline acceptance:
// `x := NewFoo(); x.Bar()` types x through NewFoo's return type and resolves
// x.Bar() to Foo.Bar at AST-grade provenance.
func TestGo_ConstructorInferenceResolvesCall(t *testing.T) {
	src := `package p

type Foo struct{ name string }

func (f *Foo) Bar() string { return f.name }

func NewFoo() *Foo { return &Foo{} }

func use() {
	x := NewFoo()
	x.Bar()
}
`
	g, dir := buildFixture(t, map[string]string{"p/p.go": src})
	runGoSpecDirect(t, g, dir)

	use := nodeByNameKind(t, g, "use", graph.KindFunction)
	bar := nodeByNameKind(t, g, "Bar", graph.KindMethod)
	e := callEdgeTo(g, use.ID, bar.ID)
	if e == nil {
		t.Fatalf("x.Bar() did not resolve to Foo.Bar (%s)", bar.ID)
	}
	assertASTProvenance(t, e, goProviderName)
}

// TestGo_ReceiverParamResolvesCall covers receiver-method resolution: a
// Foo-typed parameter resolves its `.Bar()` to the receiver method Foo.Bar.
func TestGo_ReceiverParamResolvesCall(t *testing.T) {
	src := `package p

type Foo struct{}

func (f *Foo) Bar() {}

func use(f *Foo) {
	f.Bar()
}
`
	g, dir := buildFixture(t, map[string]string{"p/p.go": src})
	runGoSpecDirect(t, g, dir)

	use := nodeByNameKind(t, g, "use", graph.KindFunction)
	bar := nodeByNameKind(t, g, "Bar", graph.KindMethod)
	e := callEdgeTo(g, use.ID, bar.ID)
	if e == nil {
		t.Fatalf("f.Bar() did not resolve to Foo.Bar (%s)", bar.ID)
	}
	assertASTProvenance(t, e, goProviderName)
}

// TestGo_TypedVarResolvesCall covers a typed local variable declaration:
// `var f Foo` then `f.Bar()` resolves on the declared type.
func TestGo_TypedVarResolvesCall(t *testing.T) {
	src := `package p

type Foo struct{}

func (f *Foo) Bar() {}

func use() {
	var f Foo
	f.Bar()
}
`
	g, dir := buildFixture(t, map[string]string{"p/p.go": src})
	runGoSpecDirect(t, g, dir)

	use := nodeByNameKind(t, g, "use", graph.KindFunction)
	bar := nodeByNameKind(t, g, "Bar", graph.KindMethod)
	if e := callEdgeTo(g, use.ID, bar.ID); e == nil {
		t.Fatalf("var f Foo; f.Bar() did not resolve to Foo.Bar (%s)", bar.ID)
	}
}

// TestGo_DoesNotDowngradeCompilerEdge is the co-running safety contract:
// when go-types has already resolved a call at LSP grade, GoSpec must leave
// that edge byte-identical — never retarget it, downgrade its tier, or
// rewrite its semantic_source.
func TestGo_DoesNotDowngradeCompilerEdge(t *testing.T) {
	src := `package p

type Foo struct{}

func (f *Foo) Bar() {}

func use() {
	var f Foo
	f.Bar()
}
`
	g, dir := buildFixture(t, map[string]string{"p/p.go": src})

	use := nodeByNameKind(t, g, "use", graph.KindFunction)
	bar := nodeByNameKind(t, g, "Bar", graph.KindMethod)

	// Simulate go-types having resolved f.Bar() to Foo.Bar at compiler grade
	// by retargeting the resolver's unresolved stub to a confirmed
	// lsp_resolved edge owned by "go-types".
	var seeded *graph.Edge
	for _, e := range g.GetOutEdges(use.ID) {
		if e.Kind == graph.EdgeCalls && trailingNameMatches(e.To, "Bar") {
			old := e.To
			e.To = bar.ID
			e.Origin = graph.OriginLSPResolved
			e.Confidence = 1.0
			e.ConfidenceLabel = "EXTRACTED"
			e.Meta = map[string]any{"semantic_source": "go-types"}
			g.ReindexEdge(e, old)
			seeded = e
			break
		}
	}
	if seeded == nil {
		t.Fatal("no f.Bar() call edge to seed as a go-types resolution")
	}

	runGoSpecDirect(t, g, dir)

	got := callEdgeTo(g, use.ID, bar.ID)
	if got == nil {
		t.Fatal("the seeded go-types edge disappeared")
	}
	if got.Origin != graph.OriginLSPResolved {
		t.Errorf("origin downgraded to %q, want %q", got.Origin, graph.OriginLSPResolved)
	}
	if src, _ := got.Meta["semantic_source"].(string); src != "go-types" {
		t.Errorf("semantic_source rewritten to %q, want %q", src, "go-types")
	}
	if got.Confidence != 1.0 {
		t.Errorf("confidence changed to %v, want 1.0", got.Confidence)
	}
	// No duplicate calls-edge to the same target was minted.
	dupes := 0
	for _, e := range g.GetOutEdges(use.ID) {
		if e.Kind == graph.EdgeCalls && e.To == bar.ID {
			dupes++
		}
	}
	if dupes != 1 {
		t.Errorf("found %d calls-edges use->Foo.Bar, want 1 (no duplication)", dupes)
	}
}

// TestGo_CrossPackageCallSkipped proves cross-package conservatism: a
// package-qualified call (`fmt.Println`) whose receiver is an imported
// package, not an in-repo type, is left untouched rather than mis-bound to a
// same-named in-repo symbol.
func TestGo_CrossPackageCallSkipped(t *testing.T) {
	src := `package p

import "fmt"

type Foo struct{}

func (f *Foo) Println() {}

func use() {
	fmt.Println("hi")
}
`
	g, dir := buildFixture(t, map[string]string{"p/p.go": src})
	runGoSpecDirect(t, g, dir)

	use := nodeByNameKind(t, g, "use", graph.KindFunction)
	// Despite an in-repo Foo.Println of the same name, the fmt.Println call
	// must stay unresolved — fmt is a package, not the type Foo.
	assertUntouched(t, g, use.ID, "Println", goProviderName)
}

// TestGo_ProviderSuppressedWhenToolchainPresent proves the toolchain-
// fallback gate: with the `go` toolchain on PATH (the normal CI case), the
// provider is a complete no-op — it adds no edges and leaves every call
// unresolved, so it can never alter go-types' results. This is the
// byte-identical safety property the integrated daemon relies on.
func TestGo_ProviderSuppressedWhenToolchainPresent(t *testing.T) {
	if !goToolchainPresent() {
		t.Skip("go toolchain absent on this host; suppression gate not exercised")
	}
	src := `package p

type Foo struct{}

func (f *Foo) Bar() {}

func use() {
	var f Foo
	f.Bar()
}
`
	g, dir := buildFixture(t, map[string]string{"p/p.go": src})

	p := NewProvider(GoSpec(), zap.NewNop())
	res, err := p.Enrich(g, dir)
	if err != nil {
		t.Fatalf("enrich: %v", err)
	}
	if res.EdgesAdded != 0 || res.EdgesConfirmed != 0 {
		t.Fatalf("suppressed provider mutated edges: %+v", res)
	}

	use := nodeByNameKind(t, g, "use", graph.KindFunction)
	for _, e := range callEdgesNamed(g, use.ID, "Bar") {
		if !graph.IsUnresolvedTarget(e.To) {
			t.Fatalf("call resolved despite suppression: %s", e.To)
		}
	}
}
