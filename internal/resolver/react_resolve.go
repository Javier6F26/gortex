package resolver

import (
	"regexp"
	"strings"

	"github.com/zzet/gortex/internal/graph"
)

// React custom-hook and context/provider name-resolution. The JS/TS extractor
// leaves three residual unresolved shapes a standard import-based resolver
// can't bind:
//
//   - a custom hook call `useAuth()` whose definition lives in a /hooks/
//     directory and reaches the call site through a path alias the resolver
//     didn't follow;
//   - a `*Context` / `*Provider` rendered in JSX (`<AuthContext.Provider>`) or
//     read via `useContext(AuthContext)`, defined under /context/ or
//     /providers/.
//
// This pass binds them by the directory-convention heuristic shared with the
// express/laravel/etc. resolvers (ResolveByConvention): a `use[A-Z]\w+`
// identifier prefers /hooks/; a `*Context`/`*Provider` prefers /context/,
// /contexts/, /providers/ with the React suffix-strip fallback (`AuthContext`
// → a definition named `Auth`). Only residual unresolved edges whose source
// file is a JS-family module are touched, so a same-named symbol in another
// language is never mis-bound. The capture side stamps `useContext` argument
// references `via=react_context` so they resolve regardless of suffix.

var (
	reactHookDirs    = []string{"/hooks/", "/hook/"}
	reactContextDirs = []string{"/context/", "/contexts/", "/providers/", "/provider/"}
	// reactHookNameRE matches the React custom-hook convention: `use`
	// followed by an upper-case letter (so `useAuth`, not the noun `user`).
	reactHookNameRE = regexp.MustCompile(`^use[A-Z][A-Za-z0-9_$]*$`)
)

// ResolveReactHooksContext binds residual unresolved React hook / context
// references to their definitions by directory convention. Returns the count
// bound.
func ResolveReactHooksContext(g graph.Store) int {
	if g == nil {
		return 0
	}
	resolved := 0
	var reindex []graph.EdgeReindex
	for _, kind := range []graph.EdgeKind{graph.EdgeCalls, graph.EdgeReferences, graph.EdgeRendersChild} {
		for e := range g.EdgesByKind(kind) {
			if e == nil || !graph.IsUnresolvedTarget(e.To) {
				continue
			}
			head := graph.UnresolvedName(e.To)
			if i := strings.IndexByte(head, '.'); i >= 0 { // member expr `AuthContext.Provider`
				head = head[:i]
			}
			via, _ := e.Meta["via"].(string)
			suffix, preferDirs, ok := reactResolveShape(head, via)
			if !ok {
				continue
			}
			fromFile := ""
			if n := g.GetNode(e.From); n != nil {
				fromFile = n.FilePath
			}
			if !isReactSourceFile(fromFile) {
				continue
			}
			targetID, conf := ResolveByConvention(g, head, suffix, preferDirs, fromFile)
			if targetID == "" {
				continue
			}
			oldTo := e.To
			e.To = targetID
			e.Origin = graph.OriginASTInferred
			e.Confidence = conf
			e.ConfidenceLabel = graph.ConfidenceLabelFor(e.Kind, conf)
			StampSynthesized(e, SynthReactResolve)
			reindex = append(reindex, graph.EdgeReindex{Edge: e, OldTo: oldTo})
			resolved++
		}
	}
	if len(reindex) > 0 {
		g.ReindexEdges(reindex)
	}
	return resolved
}

// reactResolveShape classifies an unresolved reference head into the
// (suffix, preferred-dirs) pair its convention resolution uses, or ok=false
// when the head is neither a custom hook nor a context/provider. A
// `via=react_context` tag (stamped on captured `useContext` arguments) forces
// the context shape regardless of the identifier's suffix.
func reactResolveShape(head, via string) (string, []string, bool) {
	switch {
	case via == "react_context":
		return "Context", reactContextDirs, true
	case reactHookNameRE.MatchString(head):
		return "", reactHookDirs, true
	case strings.HasSuffix(head, "Context"):
		return "Context", reactContextDirs, true
	case strings.HasSuffix(head, "Provider"):
		return "Provider", reactContextDirs, true
	}
	return "", nil, false
}

// isReactSourceFile reports whether a path is a JS-family module — the only
// files this React pass binds references from.
func isReactSourceFile(p string) bool {
	switch {
	case strings.HasSuffix(p, ".tsx"), strings.HasSuffix(p, ".jsx"),
		strings.HasSuffix(p, ".ts"), strings.HasSuffix(p, ".js"),
		strings.HasSuffix(p, ".mts"), strings.HasSuffix(p, ".cts"),
		strings.HasSuffix(p, ".mjs"), strings.HasSuffix(p, ".cjs"):
		return true
	}
	return false
}
