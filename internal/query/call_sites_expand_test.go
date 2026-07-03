package query

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/zzet/gortex/internal/graph"
)

// An edge carrying Meta["call_sites"] expands into one usage row per site.
func TestFindUsages_ExpandsCallSites(t *testing.T) {
	g := graph.New()
	target := "h.php::HandlerInterface.handle"
	caller := "app.php::run"
	g.AddNode(&graph.Node{ID: target, Kind: graph.KindMethod, Name: "handle", FilePath: "h.php", StartLine: 3, Language: "php"})
	g.AddNode(&graph.Node{ID: caller, Kind: graph.KindFunction, Name: "run", FilePath: "app.php", StartLine: 2, EndLine: 10, Language: "php"})

	e := &graph.Edge{From: caller, To: target, Kind: graph.EdgeCalls, FilePath: "app.php", Line: 5, Origin: graph.OriginLSPResolved}
	graph.AppendCallSite(e, "app.php", 7)
	graph.AppendCallSite(e, "app.php", 9)
	g.AddEdge(e)

	sg := NewEngine(g).FindUsages(target)

	var lines []int
	for _, ue := range sg.Edges {
		if ue.From == caller && ue.To == target && ue.Kind == graph.EdgeCalls {
			lines = append(lines, ue.Line)
		}
	}
	assert.ElementsMatch(t, []int{5, 7, 9}, lines, "one usage row per call site")
}

// A call_sites entry that coincides with a real per-line edge is not
// double-counted; the real edge wins.
func TestFindUsages_CallSitesDedupAgainstRealEdge(t *testing.T) {
	g := graph.New()
	target := "h.php::X.foo"
	caller := "app.php::run"
	g.AddNode(&graph.Node{ID: target, Kind: graph.KindMethod, Name: "foo", FilePath: "h.php", StartLine: 3, Language: "php"})
	g.AddNode(&graph.Node{ID: caller, Kind: graph.KindFunction, Name: "run", FilePath: "app.php", StartLine: 2, EndLine: 10, Language: "php"})

	g.AddEdge(&graph.Edge{From: caller, To: target, Kind: graph.EdgeCalls, FilePath: "app.php", Line: 7, Origin: graph.OriginASTResolved})
	e5 := &graph.Edge{From: caller, To: target, Kind: graph.EdgeCalls, FilePath: "app.php", Line: 5, Origin: graph.OriginLSPResolved}
	graph.AppendCallSite(e5, "app.php", 7) // duplicates the real edge's site
	g.AddEdge(e5)

	sg := NewEngine(g).FindUsages(target)

	countAt7 := 0
	var lines []int
	for _, ue := range sg.Edges {
		if ue.To == target && ue.Kind == graph.EdgeCalls {
			lines = append(lines, ue.Line)
			if ue.Line == 7 {
				countAt7++
			}
		}
	}
	assert.Equal(t, 1, countAt7, "site 7 must not be double-counted")
	assert.ElementsMatch(t, []int{5, 7}, lines)
}
