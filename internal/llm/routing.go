// Package llm — task-complexity classification for model routing.
//
// When llm.routing is enabled, the `ask` research agent does not run
// every request on one model. Classify scores a request by how much
// graph traversal it is likely to demand and the svc layer dispatches
// the run to a cheaper or a more capable model accordingly (see
// RoutingConfig): a trivial single-hop lookup costs Haiku, a
// cross-system trace or a refactor costs Opus.
//
// The score is "graph-derived" in that its signals describe the shape
// of the graph work the agent faces — chain-tracing mode, multi-hop
// intent in the question, and how broad a slice of the multi-repo
// graph is in scope — not just surface text length.
package llm

import "strings"

// Complexity is the routed task-complexity class of an agent run.
type Complexity int

const (
	// ComplexitySimple is a single-hop / narrowly-scoped lookup —
	// routed to RoutingConfig.SimpleModel.
	ComplexitySimple Complexity = iota
	// ComplexityComplex is a multi-hop, cross-repo, or refactor-scale
	// task — routed to RoutingConfig.ComplexModel.
	ComplexityComplex
)

// String returns the lowercase label used in AgentAnswer.Complexity.
func (c Complexity) String() string {
	if c == ComplexityComplex {
		return "complex"
	}
	return "simple"
}

// ComplexitySignals are the inputs Classify scores. They are cheap to
// gather — no LLM call, at most one already-cached graph lookup.
type ComplexitySignals struct {
	// Question is the user's natural-language query.
	Question string
	// Chain reports chain-tracing mode (cross-system call-chain).
	Chain bool
	// Scoped reports that a repo / project / ref filter narrows the
	// agent's reach to one graph partition.
	Scoped bool
	// RepoCount is the number of repos visible in the agent's scope —
	// the breadth of graph the run may have to reason over.
	RepoCount int
}

// strongComplexityKeywords signal multi-hop / whole-graph intent.
var strongComplexityKeywords = []string{
	"trace", "call chain", "callchain", "across", "end to end", "end-to-end",
	"data flow", "dataflow", "blast radius", "refactor", "every caller",
	"all callers", "everywhere", "impact of", "ripple",
}

// secondaryComplexityKeywords lean complex but are weaker on their own.
var secondaryComplexityKeywords = []string{
	"architecture", "diagram", "overview", "how does", "why does",
	"relationship", "all the", "throughout", "entire",
}

// longQuestionChars is the question length above which a request is
// treated as carrying extra complexity signal.
const longQuestionChars = 160

// Classify scores a request and returns its complexity class. The
// scoring: chain mode and a strong multi-hop keyword each add 2; a
// secondary keyword, an unscoped run over a multi-repo workspace, and
// a long question each add 1. A total of 2 or more is ComplexityComplex.
func Classify(s ComplexitySignals) Complexity {
	score := 0
	if s.Chain {
		score += 2
	}
	q := strings.ToLower(s.Question)
	for _, kw := range strongComplexityKeywords {
		if strings.Contains(q, kw) {
			score += 2
			break
		}
	}
	for _, kw := range secondaryComplexityKeywords {
		if strings.Contains(q, kw) {
			score++
			break
		}
	}
	// An unscoped run over a multi-repo workspace must consider
	// cross-repo breadth — a graph-partition signal.
	if !s.Scoped && s.RepoCount > 1 {
		score++
	}
	if len(s.Question) > longQuestionChars {
		score++
	}
	if score >= 2 {
		return ComplexityComplex
	}
	return ComplexitySimple
}
