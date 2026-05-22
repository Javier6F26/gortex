package rerank

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func TestHITSSignal_Name(t *testing.T) {
	if (HITSSignal{}).Name() != SignalHITS {
		t.Fatalf("HITSSignal.Name() = %q, want %q", (HITSSignal{}).Name(), SignalHITS)
	}
}

func TestHITSSignal_AuthorityNormalisation(t *testing.T) {
	cand := &Candidate{Node: &graph.Node{ID: "pkg::Authority"}}

	// No AuthorityOf closure -> signal sits at 0.
	if got := (HITSSignal{}).Contribute("q", cand, &Context{}); got != 0 {
		t.Errorf("no AuthorityOf closure: got %v, want 0", got)
	}

	// A pure authority (hub == 0) keeps its full normalised score.
	ctx := &Context{
		AuthorityOf: func(id string) float64 { return 0.8 },
		HubOf:       func(id string) float64 { return 0 },
	}
	if got := (HITSSignal{}).Contribute("q", cand, ctx); !floatNear(got, 0.8, 1e-9) {
		t.Errorf("pure authority: got %v, want 0.8", got)
	}

	// Authority 0 -> contribution 0.
	ctxZero := &Context{AuthorityOf: func(id string) float64 { return 0 }}
	if got := (HITSSignal{}).Contribute("q", cand, ctxZero); got != 0 {
		t.Errorf("zero authority: got %v, want 0", got)
	}
}

// TestHITSSignal_InfraPenalty is the core feature assertion: a node
// that is also a strong hub (a called-by-everything utility / an
// orchestrator) scores BELOW a true authority even when its raw
// authority value is similar -- the hub penalty divides it down.
func TestHITSSignal_InfraPenalty(t *testing.T) {
	sig := HITSSignal{}
	authorityNode := &Candidate{Node: &graph.Node{ID: "pkg::TrueAuthority"}}
	infraNode := &Candidate{Node: &graph.Node{ID: "pkg::InfraUtil"}}

	ctx := &Context{
		AuthorityOf: func(id string) float64 {
			// Both have a comparable raw authority value.
			return 0.7
		},
		HubOf: func(id string) float64 {
			// The infra utility is also a massive hub; the true
			// authority is a pure destination.
			if id == "pkg::InfraUtil" {
				return 1.0
			}
			return 0.0
		},
	}
	authScore := sig.Contribute("q", authorityNode, ctx)
	infraScore := sig.Contribute("q", infraNode, ctx)

	if infraScore >= authScore {
		t.Fatalf("a high-hub infra node (%v) must score below a true authority (%v)",
			infraScore, authScore)
	}
	// With hub == 1 the score is exactly halved.
	if !floatNear(infraScore, 0.35, 1e-9) {
		t.Errorf("infra score with hub=1 should be 0.7/(1+1)=0.35, got %v", infraScore)
	}
}

// TestHITSSignal_RegisteredInPipeline confirms the signal is wired
// into the default lineup with a tuned weight below fan_in's.
func TestHITSSignal_RegisteredInPipeline(t *testing.T) {
	found := false
	for _, s := range DefaultSignals() {
		if s.Name() == SignalHITS {
			found = true
		}
	}
	if !found {
		t.Fatal("SignalHITS missing from DefaultSignals()")
	}
	w := DefaultWeights()
	hitsW, ok := w[SignalHITS]
	if !ok {
		t.Fatal("SignalHITS missing from DefaultWeights()")
	}
	if hitsW >= w[SignalFanIn] {
		t.Errorf("HITS weight %v should sit below fan_in weight %v -- it complements, not replaces",
			hitsW, w[SignalFanIn])
	}
	if hitsW <= 0 {
		t.Errorf("HITS weight should be positive, got %v", hitsW)
	}
}
