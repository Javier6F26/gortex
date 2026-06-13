package resolver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/zzet/gortex/internal/graph"
)

type fakeVerifier struct{ verdicts map[string]TemporalVerdict }

func (f fakeVerifier) Verify(_ context.Context, req TemporalVerifyRequest) (TemporalVerifyResult, error) {
	return TemporalVerifyResult{Verdict: f.verdicts[req.DispatchName], Reason: "fake"}, nil
}

type fakeSource struct{}

func (fakeSource) NodeSource(n *graph.Node) (string, bool) { return "// src of " + n.Name, true }

type errVerifier struct{}

func (errVerifier) Verify(context.Context, TemporalVerifyRequest) (TemporalVerifyResult, error) {
	return TemporalVerifyResult{}, assert.AnError
}

func TestVerifyTemporalEdges_PromotesSuppressesKeeps(t *testing.T) {
	b := newTemporalTestGraph()
	b.addGoFunc("wf/w.go::CoreWF", "CoreWF", "wf/w.go", "svc")
	b.addGoFunc("wf/a.go::ChargeActivity", "ChargeActivity", "wf/a.go", "svc")
	b.addGoFunc("wf/a.go::FakeActivity", "FakeActivity", "wf/a.go", "svc")
	b.addGoFunc("wf/a.go::MaybeActivity", "MaybeActivity", "wf/a.go", "svc")
	b.addGoFunc("wf/a.go::SureActivity", "SureActivity", "wf/a.go", "svc")

	mkEdge := func(name, target string, conf float64) *graph.Edge {
		e := &graph.Edge{
			From: "wf/w.go::CoreWF", To: target, Kind: graph.EdgeCalls,
			Confidence: conf,
			Meta:       map[string]any{"via": "temporal.stub", "temporal_kind": "activity", "temporal_name": name},
		}
		b.g.AddEdge(e)
		return e
	}
	confirmed := mkEdge("ChargeActivity", "wf/a.go::ChargeActivity", 0.4)
	confirmed.Meta["temporal_env_source"] = "heuristic"
	confirmed.Meta[graph.MetaSpeculative] = true
	rejected := mkEdge("FakeActivity", "wf/a.go::FakeActivity", 0.6)
	rejected.Meta["temporal_resolution_via"] = "convention"
	uncertain := mkEdge("MaybeActivity", "wf/a.go::MaybeActivity", 0.5)
	registered := mkEdge("SureActivity", "wf/a.go::SureActivity", 0.9) // above band — never verified

	v := fakeVerifier{verdicts: map[string]TemporalVerdict{
		"ChargeActivity": TemporalVerdictConfirmed,
		"FakeActivity":   TemporalVerdictRejected,
		"MaybeActivity":  TemporalVerdictUncertain,
	}}

	rep := VerifyTemporalEdges(context.Background(), b.g, fakeSource{}, v)
	assert.Equal(t, 3, rep.Checked, "only the three in-band edges are verified")
	assert.Equal(t, 1, rep.Confirmed)
	assert.Equal(t, 1, rep.Rejected)
	assert.Equal(t, 1, rep.Uncertain)

	// Confirmed → promoted, visible.
	assert.Equal(t, 0.85, confirmed.Confidence)
	assert.Equal(t, "confirmed", confirmed.Meta["temporal_llm_verdict"])
	_, spec := confirmed.Meta[graph.MetaSpeculative]
	assert.False(t, spec, "confirmed edge must no longer be hidden")

	// Rejected → suppressed, hidden.
	assert.Equal(t, 0.1, rejected.Confidence)
	assert.Equal(t, true, rejected.Meta[graph.MetaSpeculative])
	assert.Equal(t, "rejected", rejected.Meta["temporal_llm_verdict"])

	// Uncertain → tier unchanged.
	assert.Equal(t, 0.5, uncertain.Confidence)
	assert.Equal(t, "uncertain", uncertain.Meta["temporal_llm_verdict"])

	// Register-confirmed → untouched.
	assert.Equal(t, 0.9, registered.Confidence)
	_, verdicted := registered.Meta["temporal_llm_verdict"]
	assert.False(t, verdicted, "register-confirmed edge must not be verified")
}

func TestVerifyTemporalEdges_ErrorLeavesEdgeUntouched(t *testing.T) {
	b := newTemporalTestGraph()
	b.addGoFunc("w.go::WF", "WF", "w.go", "svc")
	b.addGoFunc("a.go::A", "A", "a.go", "svc")
	e := &graph.Edge{
		From: "w.go::WF", To: "a.go::A", Kind: graph.EdgeCalls, Confidence: 0.4,
		Meta: map[string]any{"via": "temporal.stub", "temporal_kind": "activity", "temporal_name": "A"},
	}
	b.g.AddEdge(e)

	rep := VerifyTemporalEdges(context.Background(), b.g, fakeSource{}, errVerifier{})
	assert.Equal(t, 1, rep.Checked)
	assert.Equal(t, 1, rep.Errors)
	assert.Equal(t, 0.4, e.Confidence, "a verifier error must leave the edge untouched")
	_, verdicted := e.Meta["temporal_llm_verdict"]
	assert.False(t, verdicted)
}

func TestVerifyTemporalEdges_SkipsUnresolvedPlaceholders(t *testing.T) {
	b := newTemporalTestGraph()
	b.addGoFunc("w.go::WF", "WF", "w.go", "svc")
	// Unresolved placeholder target — nothing to verify.
	e := &graph.Edge{
		From: "w.go::WF", To: temporalStubPlaceholder("activity", "Ghost"),
		Kind: graph.EdgeCalls, Confidence: 0,
		Meta: map[string]any{"via": "temporal.stub", "temporal_kind": "activity", "temporal_name": "Ghost"},
	}
	b.g.AddEdge(e)

	rep := VerifyTemporalEdges(context.Background(), b.g, fakeSource{}, fakeVerifier{})
	assert.Equal(t, 0, rep.Checked, "unresolved placeholders are not verification candidates")
}
