package mcp

import (
	"context"

	"github.com/zzet/gortex/internal/search/rerank"
)

// buildRerankContext assembles the per-request rerank.Context with
// every session-aware data source the server holds: locality, combo,
// frecency, feedback, and churn. Pure structural signals (BM25 rank,
// fan-in / fan-out, MinHash, signature match, recency, community) do
// not depend on session state and read from the graph directly via
// the Context.Graph pointer set by the pipeline call site.
//
// Returned Context is safe to reuse for the lifetime of the request
// but should not be cached across requests — the combo boost map is
// query-specific and the locality fields are session-specific.
func (s *Server) buildRerankContext(ctx context.Context, query string) *rerank.Context {
	repo, project := s.sessionLocality(ctx)
	rctx := &rerank.Context{
		Graph:      s.graph,
		RepoPrefix: repo,
		ProjectID:  project,
	}

	if cr := s.getCommunities(); cr != nil && cr.NodeToComm != nil {
		nodeToComm := cr.NodeToComm
		rctx.CommunityOf = func(id string) string { return nodeToComm[id] }
	}

	if s.combo != nil {
		boosts := s.combo.BoostMap(query)
		if len(boosts) > 0 {
			rctx.ComboBoostOf = func(id string) float64 {
				if v, ok := boosts[id]; ok {
					return v
				}
				return 1.0
			}
		}
	}

	if s.frecency != nil && s.frecency.HasData() {
		ft := s.frecency
		rctx.FrecencyBoostOf = func(id string) float64 { return ft.BoostFor(id) }
	}

	if s.feedback != nil && s.feedback.HasData() {
		fb := s.feedback
		rctx.FeedbackOf = func(id string) float64 { return fb.GetSymbolScore(id) }
	}

	if s.symHistory != nil {
		churn := s.churnCounts()
		if len(churn) > 0 {
			rctx.ChurnOf = func(id string) int { return churn[id] }
		}
	}

	// HITS authority / hub scores feed the authority rerank signal.
	// Both closures normalise against the graph maxima so the signal
	// receives values already in [0, 1].
	if h := s.getHITS(); h != nil {
		hits := h
		maxAuth, maxHub := hits.MaxAuth, hits.MaxHub
		rctx.AuthorityOf = func(id string) float64 {
			if maxAuth <= 0 {
				return 0
			}
			return hits.AuthorityOf(id) / maxAuth
		}
		rctx.HubOf = func(id string) float64 {
			if maxHub <= 0 {
				return 0
			}
			return hits.HubOf(id) / maxHub
		}
	}

	// Co-change feeds the rerank pipeline once the git-history mine
	// has run (lazily, on the first find_co_changing_symbols call, or
	// from an enriched snapshot). Until then the signal sits at 0.
	if s.hasCoChangeData() {
		rctx.CoChangeOf = s.coChangeScores
	}

	return rctx
}
