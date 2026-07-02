package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

// A `this.<field>.<method>()` call must stamp the same receiver_type as the
// bare `<field>.<method>()` spelling, resolved through the enclosing class's
// declared field type.
func TestJavaExtractor_ThisFieldReceiverType(t *testing.T) {
	src := []byte(`public class OwnerController {
    private final OwnerRepository owners;
    public OwnerController(OwnerRepository owners) { this.owners = owners; }
    public void find() {
        this.owners.findByLastNameStartingWith("Smith");
        owners.findById(1);
    }
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("OwnerController.java", src)
	require.NoError(t, err)

	got := map[string]string{}
	for _, edge := range edgesOfKind(result.Edges, graph.EdgeCalls) {
		if edge.Meta == nil {
			continue
		}
		if rt, ok := edge.Meta["receiver_type"].(string); ok {
			got[edge.To] = rt
		}
	}
	assert.Equal(t, "OwnerRepository", got["unresolved::*.findByLastNameStartingWith"],
		"this.owners.findByLastNameStartingWith() must stamp receiver_type=OwnerRepository")
	assert.Equal(t, "OwnerRepository", got["unresolved::*.findById"],
		"owners.findById() must stamp the identical receiver_type as the this.-qualified spelling")
}

// Interface and enum method nodes keep the flat `<file>::<name>` ID but must
// carry their declaring type as Meta["receiver"] so typed call resolution can
// bind to them.
func TestJavaExtractor_InterfaceEnumMethodReceiverMeta(t *testing.T) {
	iface := []byte(`public interface OwnerRepository {
    Owner findByLastNameStartingWith(String lastName);
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("OwnerRepository.java", iface)
	require.NoError(t, err)

	var m *graph.Node
	for _, n := range result.Nodes {
		if n.Kind == graph.KindMethod && n.Name == "findByLastNameStartingWith" {
			m = n
		}
	}
	require.NotNil(t, m, "interface method node must exist")
	assert.Equal(t, "OwnerRepository.java::findByLastNameStartingWith", m.ID, "flat node ID must be preserved")
	require.NotNil(t, m.Meta)
	assert.Equal(t, "OwnerRepository", m.Meta["receiver"], "interface method node must carry receiver meta")

	enumSrc := []byte(`public enum Kind {
    A, B;
    public String label() { return name(); }
}
`)
	res2, err := e.Extract("Kind.java", enumSrc)
	require.NoError(t, err)
	var em *graph.Node
	for _, n := range res2.Nodes {
		if n.Kind == graph.KindMethod && n.Name == "label" {
			em = n
		}
	}
	require.NotNil(t, em, "enum method node must exist")
	assert.Equal(t, "Kind", em.Meta["receiver"], "enum method node must carry receiver meta")
}
