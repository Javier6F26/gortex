package semantic

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestOrderLangsByComposition pins the deterministic, primary-language-first
// provider order that replaces EnrichAll's old randomised map-range: the
// language with the most enrichable nodes runs first, ties break by name, and
// the order is stable across calls (so a Go-dominant repo's gopls always runs
// before a minor TS tree's tsserver).
func TestOrderLangsByComposition(t *testing.T) {
	providers := map[string]Provider{
		"go":         nil,
		"typescript": nil,
		"python":     nil,
	}
	// Go dominates; python and typescript are minor, python == typescript so the
	// tie must break by name (python < typescript).
	langCounts := map[string]int{"go": 5000, "typescript": 40, "python": 40}

	first := orderLangsByComposition(providers, langCounts)
	assert.Equal(t, []string{"go", "python", "typescript"}, first,
		"most enrichable nodes first; ties break by language name")

	// Determinism: the exact same inputs must yield the exact same order.
	second := orderLangsByComposition(providers, langCounts)
	assert.Equal(t, first, second, "ordering must be deterministic across calls")

	// Every registered provider language appears exactly once.
	assert.Len(t, first, len(providers))
}

// TestOrderLangsByComposition_NoCounts falls back to a name-sorted order when
// there is no composition signal (an unindexed / empty graph), so the schedule
// is still deterministic.
func TestOrderLangsByComposition_NoCounts(t *testing.T) {
	providers := map[string]Provider{"rust": nil, "go": nil, "c": nil}
	assert.Equal(t, []string{"c", "go", "rust"},
		orderLangsByComposition(providers, map[string]int{}))
}

// TestSortedRootNames pins the deterministic per-repo visit order: most
// enrichable nodes first, ties by name, and a stable name order when counts are
// absent.
func TestSortedRootNames(t *testing.T) {
	roots := map[string]string{"a": "/a", "b": "/b", "c": "/c"}

	assert.Equal(t, []string{"b", "a", "c"},
		sortedRootNames(roots, map[string]int{"a": 100, "b": 900, "c": 100}),
		"repo with the most enrichable nodes first; ties break by name")

	assert.Equal(t, []string{"a", "b", "c"},
		sortedRootNames(roots, nil),
		"no counts falls back to a stable name order")
}
