package resolver

// LLM cleaning pass for low-confidence Temporal dispatch edges.
//
// PURPOSE: the AST layers deliberately over-produce — the generic "env"-name
// heuristic, the convention fallback, and the fuzzy matcher all mint edges at
// the speculative / inferred tier to maximise recall. This pass is the
// precision backstop: it asks an LLM, grounded in the real caller + candidate
// source, whether each such edge is a true dispatch, and then PROMOTES the
// confirmed ones (visible, high confidence), SUPPRESSES the rejected ones
// (hidden), and leaves the uncertain ones where they are.
//
// RATIONALE: per-edge LLM verification is expensive, but the verifiable set is
// tiny (only resolved temporal stubs at confidence ≤ 0.65 — dozens, not the
// whole graph), so the cost is bounded. The verifier and the source provider
// are injected interfaces, so the core is deterministic and unit-testable with
// a fake LLM; the real provider + caching adapter wraps this. Register-confirmed
// edges (0.9) are never touched — the blast radius is strictly the already
// uncertain band.
//
// KEYWORDS: temporal, llm, verify, false-positive, precision, clean

import (
	"context"
	"strings"

	"github.com/zzet/gortex/internal/graph"
)

// Confidence band the verifier operates in. Resolved-but-uncertain temporal
// stubs (heuristic env-default 0.4, fuzzy 0.5, convention / inferred env-default
// 0.6) fall in (0, 0.65]; register-confirmed 0.9 edges are above it and never
// verified.
const temporalVerifyMaxConfidence = 0.65

// Confidence stamped on an edge the LLM confirmed — above the inferred band so
// it surfaces by default, but below register-confirmed 0.9 (it is an LLM
// judgement over a heuristic, not a parsed registration).
const temporalVerifyConfirmedConfidence = 0.85

// Confidence stamped on an edge the LLM rejected — floored near zero and
// flagged speculative so it drops out of default queries without deleting the
// edge (the verdict + reason ride on its meta for audit).
const temporalVerifyRejectedConfidence = 0.1

// TemporalVerdict is the LLM's judgement on a single candidate dispatch edge.
type TemporalVerdict string

const (
	TemporalVerdictConfirmed TemporalVerdict = "confirmed"
	TemporalVerdictRejected  TemporalVerdict = "rejected"
	TemporalVerdictUncertain TemporalVerdict = "uncertain"
)

// TemporalVerifyRequest is the grounded context handed to the verifier for one
// candidate edge: the dispatch name + how it was recognised, plus the source of
// the calling workflow and the candidate activity / workflow it resolved to.
type TemporalVerifyRequest struct {
	DispatchName string // the activity / workflow name being dispatched
	Kind         string // "activity" / "workflow"
	Source       string // how the AST resolved it: heuristic / convention / fuzzy / env_default / …
	CallerName   string
	CallerSource string
	TargetName   string
	TargetSource string
}

// TemporalVerifyResult is a single verdict plus a short human reason.
type TemporalVerifyResult struct {
	Verdict TemporalVerdict
	Reason  string
}

// TemporalVerifier verifies one candidate dispatch edge. The real implementation
// calls an LLM with a grounded prompt and structured output; tests inject a fake.
type TemporalVerifier interface {
	Verify(ctx context.Context, req TemporalVerifyRequest) (TemporalVerifyResult, error)
}

// TemporalSourceProvider returns the source text of a graph node (a workflow or
// activity function). The real implementation reads the node's file slice; tests
// inject an in-memory map.
type TemporalSourceProvider interface {
	NodeSource(n *graph.Node) (string, bool)
}

// TemporalVerifyDetail records the outcome for one edge (for the report / audit).
type TemporalVerifyDetail struct {
	From    string
	To      string
	Name    string
	Kind    string
	Source  string
	Verdict TemporalVerdict
	Reason  string
}

// TemporalVerifyReport summarises a verification run.
type TemporalVerifyReport struct {
	Checked   int
	Confirmed int
	Rejected  int
	Uncertain int
	Errors    int
	Details   []TemporalVerifyDetail
}

// temporalVerifiable reports whether an edge is an LLM-verification candidate: a
// resolved Temporal stub/link edge sitting in the uncertain confidence band.
func temporalVerifiable(e *graph.Edge) bool {
	if e == nil || e.Meta == nil {
		return false
	}
	via, _ := e.Meta["via"].(string)
	if !strings.HasPrefix(via, "temporal.") {
		return false
	}
	if e.To == "" || strings.HasPrefix(e.To, "unresolved::") {
		return false // unresolved placeholder — nothing to verify
	}
	return e.Confidence > 0 && e.Confidence <= temporalVerifyMaxConfidence
}

// temporalEdgeSource recovers how an edge was resolved, for the prompt + report.
func temporalEdgeSource(e *graph.Edge) string {
	if v, _ := e.Meta["temporal_env_source"].(string); v != "" {
		return "env_default:" + v
	}
	if v, _ := e.Meta["temporal_resolution_via"].(string); v != "" {
		return v
	}
	return "exact"
}

// VerifyTemporalEdges runs the LLM cleaning pass over every verifiable Temporal
// edge in g, mutating each edge's tier by the verdict and returning a report. It
// holds the graph's resolve mutex while mutating edge meta (mirroring
// ResolveTemporalCalls) so it is safe against concurrent meta readers. A
// verifier error leaves the edge untouched and is counted.
func VerifyTemporalEdges(ctx context.Context, g graph.Store, src TemporalSourceProvider, v TemporalVerifier) TemporalVerifyReport {
	var report TemporalVerifyReport
	if g == nil || src == nil || v == nil {
		return report
	}
	mu := g.ResolveMutex()
	mu.Lock()
	defer mu.Unlock()

	// Snapshot candidates first so we don't mutate while ranging the index.
	var candidates []*graph.Edge
	for e := range g.EdgesByKind(graph.EdgeCalls) {
		if temporalVerifiable(e) {
			candidates = append(candidates, e)
		}
	}

	for _, e := range candidates {
		caller := g.GetNode(e.From)
		target := g.GetNode(e.To)
		if caller == nil || target == nil {
			continue
		}
		callerSrc, _ := src.NodeSource(caller)
		targetSrc, _ := src.NodeSource(target)
		name, _ := e.Meta["temporal_name"].(string)
		kind, _ := e.Meta["temporal_kind"].(string)
		source := temporalEdgeSource(e)

		report.Checked++
		res, err := v.Verify(ctx, TemporalVerifyRequest{
			DispatchName: name,
			Kind:         kind,
			Source:       source,
			CallerName:   caller.Name,
			CallerSource: callerSrc,
			TargetName:   target.Name,
			TargetSource: targetSrc,
		})
		if err != nil {
			report.Errors++
			continue
		}

		e.Meta["temporal_llm_verdict"] = string(res.Verdict)
		if res.Reason != "" {
			e.Meta["temporal_llm_reason"] = res.Reason
		}
		switch res.Verdict {
		case TemporalVerdictConfirmed:
			e.Confidence = temporalVerifyConfirmedConfidence
			e.ConfidenceLabel = graph.ConfidenceLabelFor(graph.EdgeCalls, e.Confidence)
			delete(e.Meta, graph.MetaSpeculative)
			report.Confirmed++
		case TemporalVerdictRejected:
			e.Confidence = temporalVerifyRejectedConfidence
			e.ConfidenceLabel = graph.ConfidenceLabelFor(graph.EdgeCalls, e.Confidence)
			e.Meta[graph.MetaSpeculative] = true
			report.Rejected++
		default: // uncertain — leave the tier as-is
			report.Uncertain++
		}
		report.Details = append(report.Details, TemporalVerifyDetail{
			From: e.From, To: e.To, Name: name, Kind: kind,
			Source: source, Verdict: res.Verdict, Reason: res.Reason,
		})
	}
	return report
}
