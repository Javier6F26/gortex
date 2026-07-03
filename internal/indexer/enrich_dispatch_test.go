package indexer

import (
	"reflect"
	"testing"

	"github.com/zzet/gortex/internal/semantic"
)

// TestOrderIndexersBySpecGroup_GroupsBySpecDeterministically asserts the
// deferred-enrich dispatch order groups repos by their language set (so
// repos needing the same LSP servers run contiguously and the router's
// capped pool churns through fewer distinct provider keys), breaking ties
// by repo prefix — and that the order is stable / independent of input
// order.
func TestOrderIndexersBySpecGroup_GroupsBySpecDeterministically(t *testing.T) {
	goA := &Indexer{repoPrefix: "alpha"}
	goB := &Indexer{repoPrefix: "bravo"}
	rust := &Indexer{repoPrefix: "charlie"}
	tsA := &Indexer{repoPrefix: "delta"}
	tsB := &Indexer{repoPrefix: "echo"}

	langSets := map[*Indexer][]string{
		goA:  {"go"},
		goB:  {"go"},
		rust: {"rust"},
		tsA:  {"javascript", "typescript"},
		tsB:  {"javascript", "typescript"},
	}

	// Two different input orders must yield the same output.
	in1 := []*Indexer{tsB, goB, rust, goA, tsA}
	in2 := []*Indexer{rust, tsA, goA, tsB, goB}

	// Expected: grouped by joined-language key ("go" < "javascript,typescript"
	// < "rust"), then by repoPrefix within a group.
	want := []*Indexer{goA, goB, tsA, tsB, rust}

	got1 := orderIndexersBySpecGroup(in1, langSets)
	got2 := orderIndexersBySpecGroup(in2, langSets)

	if !reflect.DeepEqual(got1, want) {
		t.Fatalf("order mismatch for in1:\n got %s\nwant %s", prefixes(got1), prefixes(want))
	}
	if !reflect.DeepEqual(got2, want) {
		t.Fatalf("order not deterministic across input orders:\n in2 got %s\nwant %s", prefixes(got2), prefixes(want))
	}
	// Same-spec repos must be contiguous: alpha,bravo (go) then delta,echo (ts).
	if got1[0].repoPrefix != "alpha" || got1[1].repoPrefix != "bravo" {
		t.Fatalf("go repos not contiguous+ordered: %s", prefixes(got1))
	}
	if got1[2].repoPrefix != "delta" || got1[3].repoPrefix != "echo" {
		t.Fatalf("ts repos not contiguous+ordered: %s", prefixes(got1))
	}
}

// TestOrderIndexersBySpecGroup_DoesNotMutateInput confirms the helper
// returns a fresh slice and leaves the caller's slice untouched.
func TestOrderIndexersBySpecGroup_DoesNotMutateInput(t *testing.T) {
	a := &Indexer{repoPrefix: "z"}
	b := &Indexer{repoPrefix: "a"}
	in := []*Indexer{a, b}
	langSets := map[*Indexer][]string{a: {"go"}, b: {"go"}}

	_ = orderIndexersBySpecGroup(in, langSets)
	if in[0] != a || in[1] != b {
		t.Fatalf("input slice was mutated: %s", prefixes(in))
	}
}

// TestDistinctBatchSpecs_CountsIntersectingSpecs verifies the batch
// pool-raise sizing counts only enabled, available specs whose languages
// are actually present in the batch.
func TestDistinctBatchSpecs_CountsIntersectingSpecs(t *testing.T) {
	a := &Indexer{repoPrefix: "a"}
	b := &Indexer{repoPrefix: "b"}
	langSets := map[*Indexer][]string{
		a: {"go"},
		b: {"typescript"},
	}
	router := &dispatchFakeRouter{
		enabled:   []string{"gopls", "tsserver", "rust-analyzer", "gopls-broken"},
		available: map[string]bool{"gopls": true, "tsserver": true, "rust-analyzer": true, "gopls-broken": false},
		languages: map[string][]string{
			"gopls":         {"go"},
			"tsserver":      {"typescript", "javascript"},
			"rust-analyzer": {"rust"},
			"gopls-broken":  {"go"},
		},
	}
	// go → gopls, typescript → tsserver; rust-analyzer has no present
	// language; gopls-broken is unavailable. So 2 distinct specs.
	if got := distinctBatchSpecs(langSets, router); got != 2 {
		t.Fatalf("distinctBatchSpecs = %d, want 2", got)
	}

	// Empty language sets → no specs.
	if got := distinctBatchSpecs(map[*Indexer][]string{a: nil}, router); got != 0 {
		t.Fatalf("distinctBatchSpecs(empty) = %d, want 0", got)
	}
}

func prefixes(idxs []*Indexer) string {
	out := "["
	for i, idx := range idxs {
		if i > 0 {
			out += " "
		}
		out += idx.repoPrefix
	}
	return out + "]"
}

// dispatchFakeRouter is a minimal semantic.LSPRouter for exercising the
// batch dispatch helpers without spawning real LSP subprocesses.
type dispatchFakeRouter struct {
	enabled   []string
	available map[string]bool
	languages map[string][]string
	maxAlive  int
	evictions uint64
}

func (f *dispatchFakeRouter) EnabledSpecNames() []string      { return f.enabled }
func (f *dispatchFakeRouter) SpecAvailable(name string) bool  { return f.available[name] }
func (f *dispatchFakeRouter) SpecLanguages(n string) []string { return f.languages[n] }
func (f *dispatchFakeRouter) SpecPriority(string) int         { return 0 }
func (f *dispatchFakeRouter) ProviderForSpec(string) (semantic.Provider, error) {
	return nil, nil
}
func (f *dispatchFakeRouter) ProviderForSpecWorkspace(string, string) (semantic.Provider, error) {
	return nil, nil
}
func (f *dispatchFakeRouter) ReleaseSpecWorkspace(string, string) {}
func (f *dispatchFakeRouter) MaxAlive() int                       { return f.maxAlive }
func (f *dispatchFakeRouter) SetMaxAlive(n int)                   { f.maxAlive = n }
func (f *dispatchFakeRouter) EvictionCount() uint64               { return f.evictions }
func (f *dispatchFakeRouter) Close() error                        { return nil }
