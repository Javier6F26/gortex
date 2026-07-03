package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

// A selector call `x.foo()` must emit exactly one call edge carrying the
// receiver_type — not also a receiver-less plain-call edge. The two would
// collide on the edge key (which excludes Meta) and the receiver-less one would
// clobber the receiver_type, so the resolver would lose the type hint.
func TestJavaExtractor_SelectorCallNoDuplicateEdge(t *testing.T) {
	src := []byte(`public class T {
    private final Dep dep;
    void run() {
        dep.doWork();
        bare();
    }
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("T.java", src)
	require.NoError(t, err)

	var doWork []*graph.Edge
	var sawBare bool
	for _, ed := range edgesOfKind(result.Edges, graph.EdgeCalls) {
		switch ed.To {
		case "unresolved::*.doWork":
			doWork = append(doWork, ed)
		case "unresolved::*.bare":
			sawBare = true
		}
	}
	require.Len(t, doWork, 1, "a selector call must emit exactly one call edge, not a receiver-less duplicate")
	rt, _ := doWork[0].Meta["receiver_type"].(string)
	assert.Equal(t, "Dep", rt, "the surviving selector edge must keep its receiver_type")
	assert.True(t, sawBare, "a true bare call still emits its plain-call edge")
}
