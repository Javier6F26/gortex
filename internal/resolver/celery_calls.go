package resolver

import (
	"github.com/zzet/gortex/internal/graph"
)

// celeryVia is the Meta["via"] tag the Python extractor stamps on a Celery
// dispatch placeholder — a `task.delay(...)` / `.apply_async(...)` / `.s()`
// or a `send_task("name")` the static graph cannot resolve because the task
// runs out of process.
const celeryVia = "celery-dispatch"

// celeryFanoutCap bounds the candidate set a single task name may resolve
// against before the placeholder is left unbound, matching the framework's
// precision-first posture.
const celeryFanoutCap = 80

// ResolveCeleryCalls binds Celery task dispatches to the decorated task
// function: `send_email.delay(...)` → `send_email`, and
// `send_task("emails.send")` → the `@task(name="emails.send")` function.
// The decorator gate makes this precise, so edges land at the typed
// framework tier (ConfidenceTyped / ProvenanceFramework).
//
// Returns the number of placeholders landed on a real task.
func ResolveCeleryCalls(g graph.Store) int {
	if g == nil {
		return 0
	}
	byName := map[string][]*graph.Node{}
	byRegistered := map[string][]*graph.Node{}
	for _, n := range nodesByKindsOrAll(g, graph.KindFunction, graph.KindMethod) {
		if n == nil || n.Meta == nil {
			continue
		}
		if task, _ := n.Meta["celery_task"].(string); task != "" {
			byName[task] = append(byName[task], n)
		}
		if reg, _ := n.Meta["celery_registered_name"].(string); reg != "" {
			byRegistered[reg] = append(byRegistered[reg], n)
		}
	}
	if len(byName) == 0 {
		return 0
	}

	resolved := 0
	var reindex []graph.EdgeReindex
	for e := range g.EdgesByKind(graph.EdgeCalls) {
		if e == nil || e.Meta == nil {
			continue
		}
		if v, _ := e.Meta["via"].(string); v != celeryVia {
			continue
		}
		task, _ := e.Meta["celery_task"].(string)
		if task == "" {
			continue
		}
		var cands []*graph.Node
		if reg, _ := e.Meta["celery_registered_name"].(string); reg != "" {
			cands = byRegistered[reg]
		} else {
			cands = byName[task]
		}
		var target *graph.Node
		if len(cands) <= celeryFanoutCap {
			target = pickStoreAction(g, e, sameBoundaryCandidates(g, e.From, cands))
		}

		want := "unresolved::*." + task
		if target != nil {
			want = target.ID
		}
		if e.To == want {
			if target != nil {
				resolved++
			}
			continue
		}
		oldTo := e.To
		e.To = want
		if target != nil {
			e.Origin = graph.OriginASTInferred
			e.Confidence = ConfidenceTyped
			e.ConfidenceLabel = graph.ConfidenceLabelFor(graph.EdgeCalls, ConfidenceTyped)
			StampSynthesizedTyped(e, SynthCelery)
			resolved++
		} else {
			e.Origin = graph.OriginASTInferred
			e.Confidence = 0
			e.ConfidenceLabel = ""
			UnstampSynthesized(e)
		}
		reindex = append(reindex, graph.EdgeReindex{Edge: e, OldTo: oldTo})
	}
	if len(reindex) > 0 {
		g.ReindexEdges(reindex)
	}
	return resolved
}
