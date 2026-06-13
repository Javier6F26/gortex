package dataflow

import (
	"strings"

	"github.com/zzet/gortex/internal/cfg"
	"github.com/zzet/gortex/internal/graph"
)

// Refinement markers stamped on EdgeStep.Refined. A confirmed hop
// has a CFG-verified reaching-definition chain from the source
// binding to the statement that defines the target binding; a pruned
// hop is one the chain analysis disproves (the source's definition
// is killed on every path before the target's defining statement).
const (
	RefinedConfirmed = "confirmed_intraprocedural"
	RefinedPruned    = "pruned"
)

// prunedPenalty scales a path's confidence for every hop the
// reaching-definitions analysis disproves, so stale edges sink in
// the ranking instead of silently disappearing.
const prunedPenalty = 0.25

// defaultRefinerCapacity bounds how many per-function CFGs one
// refiner will hold. Refiners are per-call; the cap keeps a
// pathological many-function path from accumulating parse trees.
//
// A taint sweep reuses one refiner across every (source, sink) pair,
// and refinement runs per pair before the findings are ranked and
// capped — so a function on several candidate paths must survive in
// the cache between pairs or it gets re-parsed. The working set of a
// single flow_between walk is bounded by DefaultMaxPaths distinct
// paths of at most DefaultMaxDepth hops; sizing the cache to cover
// that union keeps a broad pattern sweep from thrashing the FIFO,
// while still bounding transient memory (the refiner is discarded at
// the end of the call).
const defaultRefinerCapacity = DefaultMaxPaths * DefaultMaxDepth

// FuncSource is one function's source text plus the file-absolute
// line its first byte sits on.
type FuncSource struct {
	Src       []byte
	StartLine int
}

// SourceResolver fetches the source of a function/method node. The
// MCP layer supplies an overlay-aware reader; tests can return
// source from memory.
type SourceResolver func(fn *graph.Node) (FuncSource, error)

// Refiner upgrades value_flow hops whose endpoints are bindings of
// the same function using statement-granular reaching-definition
// chains. CFGs are built lazily — only for functions that actually
// appear on a candidate path — and cached with FIFO eviction. Not
// safe for concurrent use; construct one per query.
type Refiner struct {
	g       graph.Store
	resolve SourceResolver
	cap     int
	entries map[string]*refEntry
	order   []string
}

// refEntry caches one function's analysis; a nil graph marks a
// negative entry (unsupported language, unreadable source, parse
// failure) so the failure isn't retried per hop.
type refEntry struct {
	c *cfg.CFG
	r *cfg.ReachingResult
}

// NewRefiner builds a refiner over the graph with the given source
// resolver. capacity <= 0 selects the default.
func NewRefiner(g graph.Store, resolve SourceResolver, capacity int) *Refiner {
	if capacity <= 0 {
		capacity = defaultRefinerCapacity
	}
	return &Refiner{
		g:       g,
		resolve: resolve,
		cap:     capacity,
		entries: make(map[string]*refEntry, capacity),
	}
}

// refinePaths stamps Refined markers on the value_flow hops it can
// judge and rescales path confidence for pruned hops. Returns true
// when any confidence changed (callers re-rank).
func (r *Refiner) refinePaths(paths []Path) bool {
	if r == nil {
		return false
	}
	changed := false
	for pi := range paths {
		for si := range paths[pi].Edges {
			step := &paths[pi].Edges[si]
			if graph.EdgeKind(step.Kind) != graph.EdgeValueFlow {
				continue
			}
			switch r.refineStep(step) {
			case RefinedConfirmed:
				step.Refined = RefinedConfirmed
			case RefinedPruned:
				step.Refined = RefinedPruned
				paths[pi].Confidence *= prunedPenalty
				changed = true
			}
		}
	}
	return changed
}

// refineStep judges one hop. Returns "" when the hop is out of scope
// (endpoints not bindings of the same function, unsupported
// language) or the CFG can't anchor both endpoints — unmarked hops
// keep their coarse-edge semantics.
func (r *Refiner) refineStep(step *EdgeStep) string {
	fromOwner, fromName, ok := splitBindingID(step.From)
	if !ok {
		return ""
	}
	toOwner, toName, ok := splitBindingID(step.To)
	if !ok || fromOwner != toOwner || fromName == "" || toName == "" {
		return ""
	}
	ent := r.entryFor(fromOwner)
	if ent == nil || ent.c == nil {
		return ""
	}

	defStmt := r.bindingDefStmt(ent, step.From, fromName)
	if defStmt == nil {
		return ""
	}
	toNode := r.g.GetNode(step.To)
	if toNode == nil || toNode.StartLine == 0 {
		return ""
	}
	useStmt := ent.c.StatementAt(toNode.StartLine, toName)
	if useStmt == nil || useStmt.Index == defStmt.Index {
		return ""
	}
	// The hop claims `toName`'s definition consumes `fromName`. If
	// the CFG statement doesn't even read fromName the extraction
	// disagrees with the graph edge — stay unmarked rather than
	// judging on mismatched evidence.
	reads := false
	for _, u := range useStmt.Uses {
		if u == fromName {
			reads = true
			break
		}
	}
	if !reads {
		return ""
	}
	for _, ch := range ent.r.ChainsFor(useStmt.Index) {
		if ch.Var != fromName {
			continue
		}
		for _, d := range ch.Defs {
			if d == defStmt.Index {
				return RefinedConfirmed
			}
		}
	}
	// fromName is read at the target statement, but the specific
	// definition this hop starts from never reaches it — every path
	// kills it first.
	return RefinedPruned
}

// bindingDefStmt anchors a binding node onto its defining CFG
// statement: params map to the synthetic entry-block param
// statements, locals to the statement covering their binding line.
func (r *Refiner) bindingDefStmt(ent *refEntry, id, name string) *cfg.Statement {
	if strings.Contains(id, "#param:") {
		for _, st := range ent.c.Stmts {
			if st.Kind != "param" {
				continue
			}
			for _, d := range st.Defs {
				if d == name {
					return st
				}
			}
		}
		return nil
	}
	node := r.g.GetNode(id)
	if node == nil || node.StartLine == 0 {
		return nil
	}
	return ent.c.StatementAt(node.StartLine, name)
}

// entryFor returns the cached analysis for a function, building it
// on first sight. Failures cache negatively.
func (r *Refiner) entryFor(ownerID string) *refEntry {
	if ent, ok := r.entries[ownerID]; ok {
		return ent
	}
	ent := &refEntry{}
	r.insert(ownerID, ent)

	fn := r.g.GetNode(ownerID)
	if fn == nil || (fn.Kind != graph.KindFunction && fn.Kind != graph.KindMethod) {
		return ent
	}
	if !cfg.SupportedLanguage(fn.Language) || fn.StartLine == 0 || fn.EndLine == 0 {
		return ent
	}
	src, err := r.resolve(fn)
	if err != nil || len(src.Src) == 0 {
		return ent
	}
	c, err := cfg.Build(src.Src, fn.Language, cfg.Options{
		LineOffset: src.StartLine - 1,
		FuncName:   fn.Name,
	})
	if err != nil {
		return ent
	}
	ent.c = c
	ent.r = c.ReachingDefinitions()
	return ent
}

func (r *Refiner) insert(key string, ent *refEntry) {
	if len(r.order) >= r.cap {
		oldest := r.order[0]
		r.order = r.order[1:]
		delete(r.entries, oldest)
	}
	r.entries[key] = ent
	r.order = append(r.order, key)
}

// splitBindingID decomposes the dataflow binding ID forms emitted at
// extraction time into the owning function ID and the variable name.
// Both binding forms may carry a position/offset suffix after `@`:
//
//   - locals: `<owner>#local:<name>@+<offsetFromOwnerStartLine>`
//   - params: `<owner>#param:<name>` (Go) or
//     `<owner>#param:<name>@<position>` (Python / TypeScript / Rust /
//     Java / C#, which disambiguate duplicate names by position).
//
// The `@…` suffix is stripped from both so the bare variable name
// matches the CFG's def/use sets; without that the param branch
// silently left every non-Go param hop unrefined.
func splitBindingID(id string) (owner, name string, ok bool) {
	if i := strings.Index(id, "#local:"); i > 0 {
		return id[:i], trimBindingSuffix(id[i+len("#local:"):]), true
	}
	if i := strings.Index(id, "#param:"); i > 0 {
		return id[:i], trimBindingSuffix(id[i+len("#param:"):]), true
	}
	return "", "", false
}

// trimBindingSuffix drops the `@<position>` / `@+<offset>`
// disambiguator a binding ID carries after its name.
func trimBindingSuffix(rest string) string {
	if j := strings.IndexByte(rest, '@'); j >= 0 {
		return rest[:j]
	}
	return rest
}
