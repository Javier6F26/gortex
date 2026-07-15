package rerank

import (
	"path"
	"strings"

	"github.com/zzet/gortex/internal/graph"
)

// lockfileBasenames enumerates the dependency-lockfile filenames whose
// `module` nodes carry no conceptual signal — they are machine-generated
// (ecosystem, name, version) tuples, not code a concept query is looking
// for. A lexical match on a package name inside one of these surfaces the
// lockfile entry above real symbols; the concept-noise demotion sinks it.
var lockfileBasenames = map[string]struct{}{
	"package-lock.json":   {},
	"npm-shrinkwrap.json": {},
	"yarn.lock":           {},
	"pnpm-lock.yaml":      {},
	"bun.lockb":           {},
	"go.sum":              {},
	"cargo.lock":          {},
	"poetry.lock":         {},
	"pipfile.lock":        {},
	"composer.lock":       {},
	"gemfile.lock":        {},
	"packages.lock.json":  {},
	"mix.lock":            {},
	"pubspec.lock":        {},
	"flake.lock":          {},
	"gradle.lockfile":     {},
}

// isLockfilePath reports whether fp is a dependency lockfile by
// basename (case-insensitive, platform-independent).
func isLockfilePath(fp string) bool {
	if fp == "" {
		return false
	}
	base := strings.ToLower(path.Base(strings.ReplaceAll(fp, "\\", "/")))
	_, ok := lockfileBasenames[base]
	return ok
}

// isConceptNoiseNode reports whether a candidate is structural noise for
// a concept-class query: a `param` / `generic_param` node (a function's
// shape, not a symbol a natural-language query is asking for), or a
// `module` node that originates from a dependency lockfile. These are
// suppressed only for concept queries and only when the caller did not
// pin an explicit kind — an explicit `kind:param` / `kind:module`
// request must still be answerable (handled by the caller before rerank).
func isConceptNoiseNode(n *graph.Node) bool {
	if n == nil {
		return false
	}
	switch n.Kind {
	case graph.KindParam, graph.KindGenericParam:
		return true
	case graph.KindModule:
		return isLockfilePath(n.FilePath)
	}
	return false
}

// demoteConceptNoise stable-partitions a score-sorted candidate slice so
// every concept-noise node (param / generic_param / lockfile module)
// trails every substantive candidate, preserving the relative order
// within each group. This guarantees the "ranked strictly below all
// substantive hits" contract for concept queries rather than relying on
// a multiplicative score nudge that a high-BM25 param could survive.
// In-place; a no-op when no noise node is present.
func demoteConceptNoise(cands []*Candidate) {
	if len(cands) < 2 {
		return
	}
	substantive := make([]*Candidate, 0, len(cands))
	noise := make([]*Candidate, 0)
	for _, c := range cands {
		if c != nil && isConceptNoiseNode(c.Node) {
			noise = append(noise, c)
			continue
		}
		substantive = append(substantive, c)
	}
	if len(noise) == 0 {
		return
	}
	copy(cands, substantive)
	copy(cands[len(substantive):], noise)
}
