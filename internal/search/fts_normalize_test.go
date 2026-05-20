package search

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withStemming pins the FTS normalization gate to a known value for
// the duration of a test and restores it afterwards, so an ambient
// GORTEX_FTS_STEMMING in the environment can't make the suite flaky.
func withStemming(t *testing.T, on bool) {
	t.Helper()
	prev := ftsStemmingEnabled
	ftsStemmingEnabled = on
	t.Cleanup(func() { ftsStemmingEnabled = prev })
}

func TestNormalizeFTSTokens_DropsStopWords(t *testing.T) {
	withStemming(t, true)
	// "the" and "is" are stopwords; "user" and "valid" survive.
	got := NormalizeFTSTokens([]string{"the", "user", "is", "valid"})
	assert.Equal(t, []string{"user", "valid"}, got)
}

func TestNormalizeFTSTokens_Disabled(t *testing.T) {
	withStemming(t, false)
	in := []string{"the", "users", "running"}
	// Gate off: passthrough, no stopword drop, no stemming.
	assert.Equal(t, in, NormalizeFTSTokens(in))
}

func TestNormalizeFTSTokens_DoesNotMutateInput(t *testing.T) {
	withStemming(t, true)
	in := []string{"users", "running"}
	_ = NormalizeFTSTokens(in)
	assert.Equal(t, []string{"users", "running"}, in)
}

func TestStemFTSToken_SymmetricVariants(t *testing.T) {
	withStemming(t, true)
	// Morphological variants must collapse to the same stem so an index
	// built from one form is reachable by a query in another.
	pairs := [][2]string{
		{"user", "users"},
		{"manage", "manager"},
		{"validate", "validator"},
		{"index", "indexing"},
	}
	for _, p := range pairs {
		assert.Equalf(t, stemFTSToken(p[0]), stemFTSToken(p[1]),
			"%q and %q must share a stem", p[0], p[1])
	}
}

func TestStemFTSToken_LeavesShortAndNonAlphaTokens(t *testing.T) {
	withStemming(t, true)
	// Short tokens and tokens with digits / non-ASCII pass through.
	for _, tok := range []string{"go", "id", "ids", "sha256", "utf8", "v2"} {
		assert.Equalf(t, tok, stemFTSToken(tok), "%q should be unchanged", tok)
	}
}

func TestBM25_StemmedRecall(t *testing.T) {
	withStemming(t, true)
	b := NewBM25()
	defer b.Close()
	b.Add("svc::UserService", "UserService", "user/service.go")
	// A query in a different grammatical number still reaches it.
	res := b.Search("users", 10)
	require.NotEmpty(t, res)
	assert.Equal(t, "svc::UserService", res[0].ID)
}

func TestBM25_StopWordQueryYieldsNoTokens(t *testing.T) {
	withStemming(t, true)
	b := NewBM25()
	defer b.Close()
	b.Add("a::Auth", "Auth", "auth.go")
	// An all-stopword query produces no FTS tokens, so BM25 returns
	// nothing and the engine layer's substring fallback takes over.
	assert.Empty(t, b.Search("the and of", 10))
}
