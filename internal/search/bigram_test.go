package search

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBigramize_ConsecutiveAndSkip(t *testing.T) {
	keys := bigramize("abcd")
	// Consecutive: ab, bc, cd. Skip-1: ac, bd. Total = 5.
	require.Len(t, keys, 5)

	// Shorter-than-2 input yields nothing.
	assert.Empty(t, bigramize("a"))
	assert.Empty(t, bigramize(""))
}

func TestBigramIndex_RoundTrip(t *testing.T) {
	bi := newBigramIndex()
	bi.Add("a", []string{"validate", "token"})
	bi.Add("b", []string{"valid", "input"})
	bi.Add("c", []string{"completely", "unrelated"})

	// The doc count is 3; density filter permits bigrams appearing in
	// [1, 2] docs — exclude any that hit all 3. None should hit all 3
	// here, so the filter is a no-op and the test is deterministic.
	cands := bi.Candidates("validate", 4)
	assert.Contains(t, cands, "a", "validate's own bigrams should put it top")

	// Removed doc disappears.
	bi.Remove("a")
	cands = bi.Candidates("validate", 4)
	assert.NotContains(t, cands, "a")
}

func TestBM25_BigramCandidates_ExplicitAPI(t *testing.T) {
	// Feature is opt-in via GORTEX_BIGRAM_TYPOS — default-off backends
	// allocate no bigram index and return nil from BigramCandidates.
	t.Setenv("GORTEX_BIGRAM_TYPOS", "1")
	// This test asserts the strict-Search contract: a clean BM25 miss
	// returns empty. The sub-word n-gram gate deliberately relaxes that
	// (a typo shares sub-word grams with the symbol), so pin it off here
	// regardless of any ambient GORTEX_SPARSE_NGRAM — the same hermetic
	// pattern withStemming uses for the FTS gate.
	withSparseNgram(t, false)

	b := NewBM25()
	defer b.Close()

	b.Add("auth/token.go::validateToken", "validateToken", "auth/token.go")
	b.Add("api/handler.go::handleRequest", "handleRequest", "api/handler.go")
	b.Add("db/store.go::openDB", "openDB", "db/store.go")

	ids := b.BigramCandidates("valiadate", 4)
	assert.Contains(t, ids, "auth/token.go::validateToken")

	// Plain Search stays strict — rescue is the engine's job, not the
	// backend's. Backend returns empty on a clean BM25 miss.
	assert.Empty(t, b.Search("valiadate", 10))
}

func TestBM25_BigramOptOut(t *testing.T) {
	// The bigram index defaults ON now — the typo-rescue tier fires only on
	// zero-result queries, so it earns its keep. A perf-sensitive operator
	// opts OUT with GORTEX_BIGRAM_TYPOS=0, restoring the zero-cost path where
	// Add/Remove cost nothing extra and BigramCandidates returns nil.
	t.Setenv("GORTEX_BIGRAM_TYPOS", "0")

	b := NewBM25()
	defer b.Close()
	b.Add("auth/token.go::validateToken", "validateToken", "auth/token.go")

	assert.Nil(t, b.BigramCandidates("valiadate", 4))
}
