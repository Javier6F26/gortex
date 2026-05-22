package rerank

// HITSSignal scores a candidate by its HITS authority -- "depended on
// by load-bearing code" -- with a hub penalty so a called-by-
// everything utility cannot ride a high in-degree to the top.
//
// It complements FanInSignal rather than replacing it. Fan-in counts
// callers; a node with many callers always scores high there. HITS
// authority is recursive: it is high only when the *callers
// themselves* are authoritative. A pure infrastructure helper
// (logging, error wrapping) has enormous fan-in but its callers are
// scattered all over the graph -- so its authority is moderate and
// its hub score is near zero. A genuine domain authority -- the
// function the orchestrators converge on -- has both high authority
// and low hub.
//
// The contribution is `authority / (1 + hub)`: a candidate that is
// also a strong hub (an orchestrator that calls many authorities,
// not a destination) is damped, since the agent searching for "what
// does the work" wants the authority, not the dispatcher.
type HITSSignal struct{}

func (HITSSignal) Name() string { return SignalHITS }

func (HITSSignal) Contribute(_ string, c *Candidate, ctx *Context) float64 {
	if ctx == nil || ctx.AuthorityOf == nil || c == nil || c.Node == nil {
		return 0
	}
	authority := ctx.AuthorityOf(c.Node.ID)
	if authority <= 0 {
		return 0
	}
	var hub float64
	if ctx.HubOf != nil {
		hub = ctx.HubOf(c.Node.ID)
	}
	// authority is already normalised into [0, 1] by buildRerankContext;
	// hub likewise. The hub penalty divides by (1 + hub), so a pure
	// authority (hub == 0) keeps its full score while a strong hub
	// (hub == 1) is halved.
	score := authority / (1.0 + hub)
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}
