package query

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser/languages"
)

// Repeated calls to the same method from the same caller on separate lines are
// distinct usages and must each surface in find_usages — call-edge identity is
// keyed on the source line, so consecutive `vet.getSpecialties()` sites do not
// collapse into a single reported usage.
func TestFindUsages_RepeatedCallSitesSurvive(t *testing.T) {
	src := []byte(`public class ClinicServiceTests {
    private Vet vet;
    void shouldFindVets() {
        vet.getSpecialties();
        vet.getSpecialties();
    }
}
`)
	e := languages.NewJavaExtractor()
	result, err := e.Extract("ClinicServiceTests.java", src)
	require.NoError(t, err)

	g := graph.New()
	// The callee lives in another file; wire it and the extracted nodes/edges.
	g.AddNode(&graph.Node{ID: "Vet.java::Vet.getSpecialties", Kind: graph.KindMethod, Name: "getSpecialties", FilePath: "Vet.java", Language: "java"})
	for _, n := range result.Nodes {
		g.AddNode(n)
	}
	// Bind each extracted getSpecialties call edge to the callee (the resolver
	// does this in the daemon; we bind directly to isolate find_usages).
	for _, ed := range result.Edges {
		if ed.Kind == graph.EdgeCalls && ed.To == "unresolved::*.getSpecialties" {
			ed.To = "Vet.java::Vet.getSpecialties"
		}
		g.AddEdge(ed)
	}

	sg := NewEngine(g).FindUsages("Vet.java::Vet.getSpecialties")
	lines := map[int]bool{}
	for _, ed := range sg.Edges {
		if ed.Kind == graph.EdgeCalls {
			lines[ed.Line] = true
		}
	}
	assert.True(t, lines[4], "the getSpecialties() call on line 4 must be a usage")
	assert.True(t, lines[5], "the second getSpecialties() call on line 5 must also be a usage (repeated sites do not collapse)")
}
