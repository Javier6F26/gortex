package exporter

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

// buildSampleGraph returns a small graph that exercises every property the
// exporters care about. Two nodes (a function calling a method) plus one
// field-write edge with extras.
func buildSampleGraph(t *testing.T) *graph.Graph {
	t.Helper()
	g := graph.New()

	g.AddNode(&graph.Node{
		ID: "main.go::F", Kind: graph.KindFunction, Name: "F",
		QualName: "example.com/m.F", FilePath: "main.go",
		StartLine: 3, EndLine: 5, Language: "go",
		RepoPrefix: "example",
		Meta: map[string]any{
			"visibility": "public",
			"doc":        "F does the thing.",
			"complexity": 4,
			"tags":       []string{"hot", "core"}, // nested → JSON-encoded
		},
	})
	g.AddNode(&graph.Node{
		ID: "lib.go::Logger.Print", Kind: graph.KindMethod, Name: "Print",
		FilePath: "lib.go", StartLine: 10, EndLine: 12, Language: "go",
		RepoPrefix: "example",
	})
	g.AddNode(&graph.Node{
		ID: "lib.go::Logger.level", Kind: graph.KindField, Name: "level",
		FilePath: "lib.go", StartLine: 8, EndLine: 8, Language: "go",
		RepoPrefix: "example",
	})

	g.AddEdge(&graph.Edge{
		From: "main.go::F", To: "lib.go::Logger.Print",
		Kind: graph.EdgeCalls, FilePath: "main.go", Line: 4,
		Confidence: 1.0, ConfidenceLabel: "EXTRACTED",
		Origin: graph.OriginASTResolved,
	})
	g.AddEdge(&graph.Edge{
		From: "main.go::F", To: "lib.go::Logger.level",
		Kind: graph.EdgeWrites, FilePath: "main.go", Line: 4,
		Confidence: 0.85, ConfidenceLabel: "INFERRED",
		Origin: graph.OriginASTInferred,
		Meta:   map[string]any{"receiver_type": "Logger"},
	})
	return g
}

func TestSnapshot_DeterministicOrder(t *testing.T) {
	g := buildSampleGraph(t)

	nodes1, edges1, _ := snapshot(g, Options{})
	nodes2, edges2, _ := snapshot(g, Options{})

	require.Equal(t, len(nodes1), len(nodes2))
	for i := range nodes1 {
		assert.Equal(t, nodes1[i].ID, nodes2[i].ID, "node order should be stable across snapshots")
	}
	require.Equal(t, len(edges1), len(edges2))
	for i := range edges1 {
		assert.Equal(t, edges1[i].From, edges2[i].From)
		assert.Equal(t, edges1[i].To, edges2[i].To)
		assert.Equal(t, edges1[i].Kind, edges2[i].Kind)
	}
}

func TestSnapshot_FiltersByKind(t *testing.T) {
	g := buildSampleGraph(t)

	nodes, edges, kept := snapshot(g, Options{Kinds: []graph.NodeKind{graph.KindFunction}})

	assert.Len(t, nodes, 1)
	assert.Equal(t, "main.go::F", nodes[0].ID)
	// Edges drop because their target endpoints are excluded.
	assert.Empty(t, edges)
	assert.True(t, kept["main.go::F"])
	assert.False(t, kept["lib.go::Logger.Print"])
}

func TestSnapshot_FiltersByLanguage(t *testing.T) {
	g := buildSampleGraph(t)
	g.AddNode(&graph.Node{
		ID: "site.py::handler", Kind: graph.KindFunction, Name: "handler",
		FilePath: "site.py", StartLine: 1, EndLine: 1, Language: "python",
	})
	nodes, _, _ := snapshot(g, Options{Languages: []string{"python"}})
	require.Len(t, nodes, 1)
	assert.Equal(t, "site.py::handler", nodes[0].ID)
}

func TestSnapshot_FiltersByRepo(t *testing.T) {
	g := buildSampleGraph(t)
	g.AddNode(&graph.Node{
		ID: "other.go::X", Kind: graph.KindFunction, Name: "X",
		RepoPrefix: "other", Language: "go",
	})
	nodes, _, _ := snapshot(g, Options{Repo: "other"})
	require.Len(t, nodes, 1)
	assert.Equal(t, "other.go::X", nodes[0].ID)
}

func TestSanitizePropertyName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"name", "name"},
		{"qual_name", "qual_name"},
		{"with-dash", "with_dash"},
		{"with space", "with_space"},
		{"123starts_with_digit", "_23starts_with_digit"},
		{"", ""},
		{"@@@", ""},
		{"meta.dotted", "meta_dotted"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, sanitizePropertyName(c.in), "input: %q", c.in)
	}
}

func TestNodeLabel_Capitalization(t *testing.T) {
	assert.Equal(t, "Function", nodeLabel(graph.KindFunction))
	assert.Equal(t, "Method", nodeLabel(graph.KindMethod))
	assert.Equal(t, "Field", nodeLabel(graph.KindField))
	assert.Equal(t, "Unknown", nodeLabel(""))
	// Multi-segment kinds should TitleCase each segment.
	assert.Equal(t, "ImportPath", nodeLabel(graph.NodeKind("import_path")))
}

func TestEdgeRelType_Uppercase(t *testing.T) {
	assert.Equal(t, "CALLS", edgeRelType(graph.EdgeCalls))
	assert.Equal(t, "WRITES", edgeRelType(graph.EdgeWrites))
	assert.Equal(t, "MEMBER_OF", edgeRelType(graph.EdgeMemberOf))
	assert.Equal(t, "RELATED", edgeRelType(""))
}

func TestFlattenMeta_StableSort(t *testing.T) {
	meta := map[string]any{
		"z_last":  1,
		"a_first": "hello",
		"middle":  true,
	}
	out := flattenMeta(meta)
	require.Len(t, out, 3)
	assert.Equal(t, "a_first", out[0].Key)
	assert.Equal(t, "middle", out[1].Key)
	assert.Equal(t, "z_last", out[2].Key)
}

func TestFlattenMeta_NestedToJSON(t *testing.T) {
	meta := map[string]any{
		"tags": []string{"a", "b"},
	}
	out := flattenMeta(meta)
	require.Len(t, out, 1)
	s, ok := out[0].Value.(string)
	require.True(t, ok, "nested slice should be flattened to a JSON string")
	assert.Contains(t, s, `"a"`)
	assert.Contains(t, s, `"b"`)
}

func TestCountingWriter(t *testing.T) {
	var buf bytes.Buffer
	cw := &countingWriter{w: &buf}
	_, err := cw.Write([]byte("hello"))
	require.NoError(t, err)
	_, err = cw.Write([]byte(" world"))
	require.NoError(t, err)
	assert.Equal(t, int64(11), cw.n)
	assert.Equal(t, "hello world", buf.String())
}

func TestSnapshot_SynthesizesStubsForExternalEndpoints(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{
		ID: "main.go::F", Kind: graph.KindFunction, Name: "F",
		FilePath: "main.go", StartLine: 1, EndLine: 5, Language: "go",
	})
	// Edge to an external symbol that isn't a real graph node.
	g.AddEdge(&graph.Edge{
		From: "main.go::F", To: "external::error", Kind: graph.EdgeThrows,
		Confidence: 1.0, ConfidenceLabel: "EXTRACTED",
	})
	// Edge to an unresolved import.
	g.AddEdge(&graph.Edge{
		From: "main.go::F", To: "unresolved::lib.SomeFunc", Kind: graph.EdgeCalls,
		Confidence: 0.8, ConfidenceLabel: "INFERRED",
	})

	nodes, edges, _ := snapshot(g, Options{})

	// Expect: original F + 2 synthesized stubs.
	require.Len(t, nodes, 3)
	require.Len(t, edges, 2)

	var sawError, sawUnresolved bool
	for _, n := range nodes {
		if n.ID == "external::error" {
			sawError = true
			assert.Equal(t, graph.NodeKind("external"), n.Kind)
			assert.Equal(t, "error", n.Name)
			assert.Equal(t, true, n.Meta["synthetic"])
		}
		if n.ID == "unresolved::lib.SomeFunc" {
			sawUnresolved = true
			assert.Equal(t, graph.NodeKind("unresolved"), n.Kind)
			assert.Equal(t, "lib.SomeFunc", n.Name)
		}
	}
	assert.True(t, sawError, "expected synthetic node for external::error")
	assert.True(t, sawUnresolved, "expected synthetic node for unresolved::lib.SomeFunc")
}

func TestSnapshot_DropSyntheticOmitsStubs(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{
		ID: "main.go::F", Kind: graph.KindFunction, Name: "F",
		FilePath: "main.go", StartLine: 1, EndLine: 5, Language: "go",
	})
	g.AddEdge(&graph.Edge{
		From: "main.go::F", To: "external::error", Kind: graph.EdgeThrows,
	})

	nodes, edges, _ := snapshot(g, Options{DropSynthetic: true})

	require.Len(t, nodes, 1)
	assert.Equal(t, "main.go::F", nodes[0].ID)
	assert.Empty(t, edges, "edge to a stub should be dropped under DropSynthetic")
}

// Smoke test: run both exporters on the sample graph and assert they don't
// crash. Detailed assertions live in the format-specific test files.
func TestExporters_Smoke(t *testing.T) {
	g := buildSampleGraph(t)

	var cypherBuf bytes.Buffer
	stats, err := WriteCypher(&cypherBuf, g, Options{})
	require.NoError(t, err)
	assert.Equal(t, 3, stats.NodesWritten)
	assert.Equal(t, 2, stats.EdgesWritten)
	assert.Greater(t, stats.BytesWritten, int64(0))
	assert.Contains(t, cypherBuf.String(), "CREATE")

	var graphmlBuf bytes.Buffer
	stats, err = WriteGraphML(&graphmlBuf, g, Options{})
	require.NoError(t, err)
	assert.Equal(t, 3, stats.NodesWritten)
	assert.Equal(t, 2, stats.EdgesWritten)
	assert.True(t, strings.HasPrefix(graphmlBuf.String(), `<?xml`))
	assert.Contains(t, graphmlBuf.String(), "<graphml")
}
