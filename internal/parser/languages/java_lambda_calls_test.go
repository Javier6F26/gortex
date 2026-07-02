package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

// Calls inside lambda bodies — expression form, block form, and lambdas in
// argument position — must emit call edges attributed to the enclosing named
// method, exactly like top-level statements. A test that only exercises the
// caller through a lambda (assertThatThrownBy(() -> x.y())) is a real usage of
// x.y() and must not vanish.
func TestJavaExtractor_LambdaBodyCalls(t *testing.T) {
	src := []byte(`public class Fixture {
    void run() {
        Runnable r = () -> foo.bar();
        assertThatExceptionOfType(RuntimeException.class).isThrownBy(() -> testee.triggerException());
        assertThat(values).allMatch(value -> value.getId() != null);
        list.forEach(item -> { process(item); item.finish(); });
    }
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("Fixture.java", src)
	require.NoError(t, err)

	targets := map[string]bool{}
	for _, edge := range edgesOfKind(result.Edges, graph.EdgeCalls) {
		if edge.From == "Fixture.java::Fixture.run" {
			targets[edge.To] = true
		}
	}
	for _, want := range []string{
		"unresolved::*.bar",              // expression lambda in a local init
		"unresolved::*.triggerException", // lambda in argument position
		"unresolved::*.getId",            // argument-position lambda with a param
		"unresolved::*.process",          // block-bodied lambda, statement 1
		"unresolved::*.finish",           // block-bodied lambda, statement 2
	} {
		assert.True(t, targets[want], "expected a call edge to %s attributed to the enclosing method", want)
	}
}
