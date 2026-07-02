package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

// `new Foo(arg)` inside a method must emit a calls candidate targeting the
// flat `Foo.<init>` constructor node, alongside the existing instantiates
// edge. The candidate is class-qualified so resolution binds it precisely.
func TestJavaExtractor_ConstructorCallEdge(t *testing.T) {
	src := []byte(`public class Fixture {
    void run() {
        PetTypeFormatter f = new PetTypeFormatter(types);
        var v = new Visit();
        doThing(new Owner("x"));
    }
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("Fixture.java", src)
	require.NoError(t, err)

	ctorCalls := map[string]string{} // target -> receiver_type
	var sawInstantiate bool
	for _, edge := range result.Edges {
		if edge.Kind == graph.EdgeCalls && edge.From == "Fixture.java::Fixture.run" {
			if rt, _ := edge.Meta["via"].(string); rt == "constructor" {
				ctorCalls[edge.To], _ = edge.Meta["receiver_type"].(string)
			}
		}
		if edge.Kind == graph.EdgeInstantiates {
			sawInstantiate = true
		}
	}
	assert.Equal(t, "PetTypeFormatter", ctorCalls["unresolved::*.PetTypeFormatter.<init>"],
		"new PetTypeFormatter(types) must emit a constructor calls candidate")
	assert.Contains(t, ctorCalls, "unresolved::*.Visit.<init>", "new Visit() must emit a constructor calls candidate")
	assert.Contains(t, ctorCalls, "unresolved::*.Owner.<init>", "argument-position new Owner() must emit a constructor calls candidate")
	assert.True(t, sawInstantiate, "the instantiates edge must still be emitted alongside the calls candidate")
}
