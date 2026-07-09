package rerank

import (
	"math"
	"strings"
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

// bagEmbed is a deterministic stub embedder for the ranking regression
// tests: it maps text to a sparse bag-of-words vector over a fixed
// vocabulary, so cosine similarity reflects shared word tokens without
// depending on the baked GloVe data. Case-folded, split on non-letters.
func bagEmbed(vocab []string) func(string) []float32 {
	idx := make(map[string]int, len(vocab))
	for i, w := range vocab {
		idx[w] = i
	}
	return func(text string) []float32 {
		v := make([]float32, len(vocab))
		for _, tok := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
			return !(r >= 'a' && r <= 'z')
		}) {
			if i, ok := idx[tok]; ok {
				v[i] += 1
			}
		}
		// normalise
		var n float64
		for _, x := range v {
			n += float64(x) * float64(x)
		}
		if n == 0 {
			return v
		}
		n = math.Sqrt(n)
		for i := range v {
			v[i] /= float32(n)
		}
		return v
	}
}

func TestSemanticCosineSignal_RewardsSharedVocabulary(t *testing.T) {
	vocab := []string{"decode", "bson", "body", "request", "json", "render", "html"}
	embed := bagEmbed(vocab)
	ctx := &Context{EmbedText: embed, QueryVec: embed("decode bson request body")}

	related := &Candidate{Node: &graph.Node{Name: "BindBody", QualName: "bsonBinding.BindBody", FilePath: "binding/bson.go"}}
	unrelated := &Candidate{Node: &graph.Node{Name: "RenderHTML", QualName: "htmlRender.RenderHTML", FilePath: "render/html.go"}}

	sig := SemanticCosineSignal{}
	rs := sig.Contribute("", related, ctx)
	us := sig.Contribute("", unrelated, ctx)
	if rs <= us {
		t.Fatalf("expected the bson-body candidate to out-score the html candidate: related=%.3f unrelated=%.3f", rs, us)
	}
	if rs <= 0 {
		t.Fatalf("expected a positive semantic score for the shared-vocabulary candidate, got %.3f", rs)
	}
}

func TestSemanticCosineSignal_ZeroWithoutEmbedder(t *testing.T) {
	c := &Candidate{Node: &graph.Node{Name: "BindBody", FilePath: "binding/bson.go"}}
	if got := (SemanticCosineSignal{}).Contribute("", c, &Context{}); got != 0 {
		t.Fatalf("no embedder wired should contribute 0, got %.3f", got)
	}
}

func TestSupportFileDemotion_ConceptOnly(t *testing.T) {
	testCand := &Candidate{Node: &graph.Node{Name: "encode", FilePath: "tests/test_content.py"}}
	implCand := &Candidate{Node: &graph.Node{Name: "encode", FilePath: "httpx/_content.py"}}

	concept := &Context{QueryClass: QueryClassConcept}
	if d := supportFileDemotion(testCand, concept); d >= 1.0 {
		t.Fatalf("a test file on a concept query must be demoted (<1), got %.3f", d)
	}
	if d := supportFileDemotion(implCand, concept); d != 1.0 {
		t.Fatalf("a non-test file must not be demoted, got %.3f", d)
	}
	// An identifier query leaves the exact-token ordering untouched.
	symbol := &Context{QueryClass: QueryClassSymbol}
	if d := supportFileDemotion(testCand, symbol); d != 1.0 {
		t.Fatalf("a symbol query must not demote a test file, got %.3f", d)
	}
}

func TestSupportFileDemotion_CannotLowerNonTest(t *testing.T) {
	// The invariant the feature relies on: only test scores drop, so a
	// non-test candidate's demotion factor is always exactly 1.0.
	for _, fp := range []string{"httpx/_content.py", "binding/bson.go", "internal/auth/token.go"} {
		c := &Candidate{Node: &graph.Node{Name: "x", FilePath: fp}}
		if d := supportFileDemotion(c, &Context{QueryClass: QueryClassConcept}); d != 1.0 {
			t.Fatalf("non-test %q must keep factor 1.0, got %.3f", fp, d)
		}
	}
}

func TestOverloadProminence_FiresOnlyOnCollision(t *testing.T) {
	sig := OverloadProminenceSignal{}
	unique := &Candidate{Node: &graph.Node{Name: "SoleName", Kind: graph.KindMethod, FilePath: "a.go", Language: "go"}}
	ctx := &Context{QueryClass: QueryClassSymbol, nameGroupCount: map[string]int{"solename": 1}}
	if got := sig.Contribute("", unique, ctx); got != 0 {
		t.Fatalf("a non-colliding name must contribute 0, got %.3f", got)
	}

	// A concept query never fires the overload signal — it must not
	// fight the semantic channel.
	ctxConcept := &Context{QueryClass: QueryClassConcept, nameGroupCount: map[string]int{"decode": 2}}
	collide := &Candidate{Node: &graph.Node{Name: "Decode", Kind: graph.KindMethod, FilePath: "codec/x.go", Language: "go"}}
	if got := sig.Contribute("", collide, ctxConcept); got != 0 {
		t.Fatalf("a concept query must not fire the overload signal, got %.3f", got)
	}

	// Two same-named candidates on an identifier query: the exported,
	// non-test method beats the test-file one.
	ctx2 := &Context{QueryClass: QueryClassSymbol, nameGroupCount: map[string]int{"decode": 2}}
	exported := &Candidate{Node: &graph.Node{Name: "Decode", Kind: graph.KindMethod, FilePath: "codec/x.go", Language: "go"}}
	private := &Candidate{Node: &graph.Node{Name: "Decode", Kind: graph.KindMethod, FilePath: "codec/x_test.go", Language: "go"}}
	if sig.Contribute("", exported, ctx2) <= sig.Contribute("", private, ctx2) {
		t.Fatalf("exported non-test overload must out-score the test-file overload")
	}
}

func TestApplyFileDiversity_BoundedDemotion(t *testing.T) {
	mk := func(id, fp string) *Candidate {
		return &Candidate{Node: &graph.Node{ID: id, FilePath: fp}}
	}
	// Five rows from big.go crowd the head; small.go sits at rank 5.
	cands := []*Candidate{
		mk("b1", "big.go"), mk("b2", "big.go"), mk("b3", "big.go"),
		mk("b4", "big.go"), mk("b5", "big.go"), mk("s1", "small.go"),
	}
	applyFileDiversity(cands)
	// The first two big.go rows are untouched; the third-and-later are
	// demoted so small.go's best row enters the head.
	if cands[0].Node.ID != "b1" || cands[1].Node.ID != "b2" {
		t.Fatalf("top-2 of the crowding file must keep their rank, got %s,%s",
			cands[0].Node.ID, cands[1].Node.ID)
	}
	pos := map[string]int{}
	for i, c := range cands {
		pos[c.Node.ID] = i
	}
	if pos["s1"] >= pos["b4"] {
		t.Fatalf("fresh file's best row must outrank the 4th crowding row: %v", pos)
	}
	// A list with no file holding 3+ rows is untouched.
	orig := []*Candidate{mk("a", "x.go"), mk("b", "y.go"), mk("c", "x.go"), mk("d", "y.go")}
	want := []string{"a", "b", "c", "d"}
	applyFileDiversity(orig)
	for i, c := range orig {
		if c.Node.ID != want[i] {
			t.Fatalf("no-crowding list must be stable, got %v at %d", c.Node.ID, i)
		}
	}
}

// TestPipeline_SemanticLiftsConceptTarget locks in the end-to-end
// behaviour: with the semantic channel wired, a concept query lifts the
// semantically-related candidate above a lexically-adjacent but unrelated
// one that BM25 alone ranked higher.
func TestPipeline_SemanticLiftsConceptTarget(t *testing.T) {
	vocab := []string{"decode", "bson", "body", "request", "render", "html", "list"}
	embed := bagEmbed(vocab)
	p := NewDefault()

	// The unrelated candidate leads on BM25 (rank 0); the real target is
	// rank 3 but semantically matches the query.
	unrelated := &Candidate{Node: &graph.Node{ID: "u", Name: "BodyList", FilePath: "list/body.go"}, TextRank: 0, VectorRank: -1}
	target := &Candidate{Node: &graph.Node{ID: "t", Name: "BindBody", QualName: "bsonBinding.BindBody", FilePath: "binding/bson.go"}, TextRank: 3, VectorRank: -1}
	cands := []*Candidate{unrelated, target}

	ctx := &Context{
		QueryClass: QueryClassConcept,
		EmbedText:  embed,
		QueryVec:   embed("decode bson request body"),
	}
	out := p.Rerank("decode bson request body", cands, ctx)
	if out[0].Node.ID != "t" {
		t.Fatalf("expected the semantically-matching target to rank first, got %q (scores t=%.3f u=%.3f)",
			out[0].Node.ID, target.Score, unrelated.Score)
	}
}
