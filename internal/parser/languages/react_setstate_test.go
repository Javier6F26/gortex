package languages

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func TestReactSetState_EmitsSetStateCallEdge(t *testing.T) {
	src := `class Counter extends React.Component {
  increment() {
    this.setState({ count: this.state.count + 1 });
  }
  render() {
    return null;
  }
}
`
	res, err := NewTypeScriptExtractor().Extract("Counter.tsx", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	var hasSetStateCall, hasRenderMember bool
	for _, e := range res.Edges {
		if e.Kind == graph.EdgeCalls && e.From == "Counter.tsx::Counter.increment" && isSetStateLikeTarget(e.To) {
			hasSetStateCall = true
		}
		if e.Kind == graph.EdgeMemberOf && e.From == "Counter.tsx::Counter.render" && e.To == "Counter.tsx::Counter" {
			hasRenderMember = true
		}
	}
	if !hasSetStateCall {
		t.Errorf("expected a setState call edge from increment")
	}
	if !hasRenderMember {
		t.Errorf("expected render member_of Counter")
	}
}

func isSetStateLikeTarget(to string) bool {
	return len(to) >= 9 && to[len(to)-9:] == ".setState"
}
