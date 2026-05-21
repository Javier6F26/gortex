package llm

import (
	"strings"
	"testing"
)

func TestComplexity_String(t *testing.T) {
	if ComplexitySimple.String() != "simple" {
		t.Errorf("ComplexitySimple.String() = %q", ComplexitySimple.String())
	}
	if ComplexityComplex.String() != "complex" {
		t.Errorf("ComplexityComplex.String() = %q", ComplexityComplex.String())
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		sig  ComplexitySignals
		want Complexity
	}{
		{
			name: "plain single-hop lookup",
			sig:  ComplexitySignals{Question: "who calls NewServer", Scoped: true, RepoCount: 1},
			want: ComplexitySimple,
		},
		{
			name: "chain mode is always complex",
			sig:  ComplexitySignals{Question: "find the handler", Chain: true},
			want: ComplexityComplex,
		},
		{
			name: "strong multi-hop keyword",
			sig:  ComplexitySignals{Question: "trace the request across systems", Scoped: true},
			want: ComplexityComplex,
		},
		{
			name: "refactor keyword",
			sig:  ComplexitySignals{Question: "refactor the auth package", Scoped: true},
			want: ComplexityComplex,
		},
		{
			name: "secondary keyword alone is not enough",
			sig:  ComplexitySignals{Question: "give me an overview", Scoped: true, RepoCount: 1},
			want: ComplexitySimple,
		},
		{
			name: "secondary keyword plus unscoped multi-repo breadth",
			sig:  ComplexitySignals{Question: "give me an architecture overview", Scoped: false, RepoCount: 5},
			want: ComplexityComplex,
		},
		{
			name: "unscoped multi-repo alone is not enough",
			sig:  ComplexitySignals{Question: "where is parseJWT", Scoped: false, RepoCount: 8},
			want: ComplexitySimple,
		},
		{
			name: "long question plus secondary keyword",
			sig: ComplexitySignals{
				Question: "explain how does the indexer keep the graph fresh when files change on disk " +
					"and the watcher fires repeatedly during a large rebase operation that rewrites " +
					"hundreds of files at once across the whole working tree",
				Scoped: true, RepoCount: 1,
			},
			want: ComplexityComplex,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.sig); got != c.want {
				t.Errorf("Classify = %v, want %v", got, c.want)
			}
		})
	}
}

func TestClassify_KeywordsCaseInsensitive(t *testing.T) {
	got := Classify(ComplexitySignals{Question: "TRACE the call CHAIN", Scoped: true})
	if got != ComplexityComplex {
		t.Errorf("Classify with upper-case keywords = %v, want complex", got)
	}
}

func TestClassify_LongQuestionThreshold(t *testing.T) {
	// A long question alone scores 1 — below the threshold of 2.
	long := strings.Repeat("a ", longQuestionChars)
	if got := Classify(ComplexitySignals{Question: long, Scoped: true}); got != ComplexitySimple {
		t.Errorf("a long but otherwise-simple question = %v, want simple", got)
	}
}
