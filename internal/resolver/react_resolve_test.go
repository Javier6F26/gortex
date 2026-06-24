package resolver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

// reactComponent adds a component function node.
func reactComponent(g *graph.Graph, id, file string) {
	g.AddNode(&graph.Node{ID: id, Kind: graph.KindFunction, Name: "Component", FilePath: file, Language: "typescript"})
}

// reactEdge adds an unresolved reference edge of the given kind.
func reactEdge(g *graph.Graph, from, file, to string, kind graph.EdgeKind, meta map[string]any) {
	g.AddEdge(&graph.Edge{From: from, To: to, Kind: kind, FilePath: file, Meta: meta})
}

// synthReactEdge returns the React-synthesized edge from→to, or nil.
func synthReactEdge(g graph.Store, kind graph.EdgeKind, from, to string) *graph.Edge {
	for e := range g.EdgesByKind(kind) {
		if e == nil || e.From != from || e.To != to || e.Meta == nil {
			continue
		}
		if by, _ := e.Meta[MetaSynthesizedBy].(string); by == SynthReactResolve {
			return e
		}
	}
	return nil
}

func TestResolveReactHooksContext_FixtureTree(t *testing.T) {
	g := graph.New()
	const comp = "src/components/Profile.tsx::Profile"
	reactComponent(g, comp, "src/components/Profile.tsx")

	// useAuth: a /hooks/ definition plus a decoy in /utils/. The /hooks/
	// preference must beat the decoy.
	convNode(g, "src/hooks/useAuth.ts::useAuth", "src/hooks/useAuth.ts", "useAuth")
	convNode(g, "src/utils/useAuth.ts::useAuth", "src/utils/useAuth.ts", "useAuth")
	// AuthContext: the definition is named `Auth` (suffix-stripped) under
	// /context/, exercising the React suffix-strip fallback.
	convNode(g, "src/context/AuthContext.tsx::Auth", "src/context/AuthContext.tsx", "Auth")

	// Hook call, useContext-captured reference (via=react_context), and the
	// JSX member-expression render edge.
	reactEdge(g, comp, "src/components/Profile.tsx", "unresolved::useAuth", graph.EdgeCalls, map[string]any{})
	reactEdge(g, comp, "src/components/Profile.tsx", "unresolved::AuthContext", graph.EdgeReferences, map[string]any{"via": "react_context"})
	reactEdge(g, comp, "src/components/Profile.tsx", "unresolved::AuthContext.Provider", graph.EdgeRendersChild, map[string]any{"child_name": "AuthContext.Provider"})

	n := ResolveReactHooksContext(g)
	require.Equal(t, 3, n)

	// Hook binds to /hooks/, not the /utils/ decoy.
	hook := synthReactEdge(g, graph.EdgeCalls, comp, "src/hooks/useAuth.ts::useAuth")
	require.NotNil(t, hook, "useAuth binds to /hooks/useAuth.ts")
	assert.Nil(t, synthReactEdge(g, graph.EdgeCalls, comp, "src/utils/useAuth.ts::useAuth"))

	// useContext reference resolves via the suffix-strip fallback.
	assert.NotNil(t, synthReactEdge(g, graph.EdgeReferences, comp, "src/context/AuthContext.tsx::Auth"),
		"useContext(AuthContext) binds to /context/ with suffix-strip")
	// JSX `<AuthContext.Provider>` member-expr render edge resolves to the same def.
	assert.NotNil(t, synthReactEdge(g, graph.EdgeRendersChild, comp, "src/context/AuthContext.tsx::Auth"),
		"<AuthContext.Provider> binds to /context/")
}

func TestResolveReactHooksContext_AmbiguousLeftAlone(t *testing.T) {
	g := graph.New()
	const comp = "src/components/Panel.tsx::Panel"
	reactComponent(g, comp, "src/components/Panel.tsx")

	// Two useTheme definitions, neither under /hooks/ nor the caller's dir →
	// ambiguous, must stay unresolved.
	convNode(g, "src/a/useTheme.ts::useTheme", "src/a/useTheme.ts", "useTheme")
	convNode(g, "src/b/useTheme.ts::useTheme", "src/b/useTheme.ts", "useTheme")
	reactEdge(g, comp, "src/components/Panel.tsx", "unresolved::useTheme", graph.EdgeCalls, map[string]any{})

	require.Equal(t, 0, ResolveReactHooksContext(g))
	assert.Nil(t, synthReactEdge(g, graph.EdgeCalls, comp, "src/a/useTheme.ts::useTheme"))
	assert.Nil(t, synthReactEdge(g, graph.EdgeCalls, comp, "src/b/useTheme.ts::useTheme"))
}

func TestResolveReactHooksContext_NonJSLeftAlone(t *testing.T) {
	g := graph.New()
	// A Go caller referencing a same-named symbol must never bind through the
	// React pass — the JS-family file gate blocks it.
	const goFn = "pkg/svc.go::useThing"
	g.AddNode(&graph.Node{ID: goFn, Kind: graph.KindFunction, Name: "useThing", FilePath: "pkg/svc.go", Language: "go"})
	convNode(g, "src/hooks/useThing.ts::useThing", "src/hooks/useThing.ts", "useThing")
	reactEdge(g, goFn, "pkg/svc.go", "unresolved::useThing", graph.EdgeCalls, map[string]any{})

	require.Equal(t, 0, ResolveReactHooksContext(g))
	assert.Nil(t, synthReactEdge(g, graph.EdgeCalls, goFn, "src/hooks/useThing.ts::useThing"))
}
