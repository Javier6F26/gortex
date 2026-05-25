package indexer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// TestShouldIndexForSearch_ExcludesKindLocal is the regression that
// guards the search-index default-filter for KindLocal. The Go
// dataflow walker materialises every intra-function binding as a
// KindLocal node; without the search-side exclusion, common names
// (`err` / `data` / `n` / `i`) would flood every search result with
// thousands of per-function copies.
func TestShouldIndexForSearch_ExcludesKindLocal(t *testing.T) {
	idx := New(graph.New(), parser.NewRegistry(), config.Default().Index, zap.NewNop())

	cases := []struct {
		name string
		node *graph.Node
		want bool
	}{
		{"function passes", &graph.Node{ID: "f", Kind: graph.KindFunction, Name: "Foo"}, true},
		{"method passes", &graph.Node{ID: "m", Kind: graph.KindMethod, Name: "Bar"}, true},
		{"type passes", &graph.Node{ID: "t", Kind: graph.KindType, Name: "Baz"}, true},
		{"param passes", &graph.Node{ID: "p", Kind: graph.KindParam, Name: "x"}, true},
		{"closure passes", &graph.Node{ID: "c", Kind: graph.KindClosure, Name: "closure@4"}, true},
		{"file excluded", &graph.Node{ID: "fl", Kind: graph.KindFile, Name: "foo.go"}, false},
		{"import excluded", &graph.Node{ID: "im", Kind: graph.KindImport, Name: "fmt"}, false},
		{"local excluded — the regression", &graph.Node{ID: "l", Kind: graph.KindLocal, Name: "err"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := idx.shouldIndexForSearch(c.node)
			assert.Equal(t, c.want, got)
		})
	}
}
