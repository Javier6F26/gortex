package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/analysis"
	"github.com/zzet/gortex/internal/graph"
)

func newCouplingTestServer(t *testing.T) *Server {
	t.Helper()
	g := graph.New()
	// Three packages: api/, service/, repo/.
	// api → service → repo (one-way dep chain).
	g.AddNode(&graph.Node{ID: "api/h.go::Handle", Name: "Handle", Kind: graph.KindFunction, FilePath: "api/h.go", Language: "go"})
	g.AddNode(&graph.Node{ID: "service/s.go::Process", Name: "Process", Kind: graph.KindFunction, FilePath: "service/s.go", Language: "go"})
	g.AddNode(&graph.Node{ID: "repo/r.go::Fetch", Name: "Fetch", Kind: graph.KindFunction, FilePath: "repo/r.go", Language: "go"})

	g.AddEdge(&graph.Edge{From: "api/h.go::Handle", To: "service/s.go::Process", Kind: graph.EdgeCalls})
	g.AddEdge(&graph.Edge{From: "service/s.go::Process", To: "repo/r.go::Fetch", Kind: graph.EdgeCalls})

	return &Server{
		graph:      g,
		session:    newSessionState(),
		tokenStats: &tokenStats{},
		symHistory: &symbolHistory{entries: make(map[string][]SymbolModification)},
		sessions:   newSessionMap(),
		toolScopes: newScopeRegistry(),
	}
}

func callCouplingHandler(t *testing.T, s *Server, args map[string]any) map[string]any {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	res, err := s.handleGetCouplingMetrics(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.False(t, res.IsError)
	tc, ok := res.Content[0].(mcp.TextContent)
	require.True(t, ok)
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(tc.Text), &m))
	return m
}

func findCouplingRow(out map[string]any, unit string) map[string]any {
	rows, _ := out["units"].([]any)
	for _, r := range rows {
		m := r.(map[string]any)
		if m["unit"] == unit {
			return m
		}
	}
	return nil
}

func TestCoupling_PackageMetricsCorrect(t *testing.T) {
	s := newCouplingTestServer(t)
	out := callCouplingHandler(t, s, map[string]any{"package_depth": 1})

	api := findCouplingRow(out, "api")
	service := findCouplingRow(out, "service")
	repo := findCouplingRow(out, "repo")
	require.NotNil(t, api)
	require.NotNil(t, service)
	require.NotNil(t, repo)

	// api: no incoming, depends on service. ca=0, ce=1, instability=1.
	assert.EqualValues(t, 0, api["ca"].(float64))
	assert.EqualValues(t, 1, api["ce"].(float64))
	assert.InDelta(t, 1.0, api["instability"].(float64), 1e-6)

	// service: depended on by api, depends on repo. ca=1, ce=1, instability=0.5.
	assert.EqualValues(t, 1, service["ca"].(float64))
	assert.EqualValues(t, 1, service["ce"].(float64))
	assert.InDelta(t, 0.5, service["instability"].(float64), 1e-6)

	// repo: depended on by service, depends on nothing. ca=1, ce=0, instability=0.
	assert.EqualValues(t, 1, repo["ca"].(float64))
	assert.EqualValues(t, 0, repo["ce"].(float64))
	assert.InDelta(t, 0.0, repo["instability"].(float64), 1e-6)
}

func TestCoupling_InternalEdgesNotInCaCe(t *testing.T) {
	s := newCouplingTestServer(t)
	// Add an intra-package call inside service/.
	s.graph.AddNode(&graph.Node{ID: "service/u.go::Util", Name: "Util", Kind: graph.KindFunction, FilePath: "service/u.go", Language: "go"})
	s.graph.AddEdge(&graph.Edge{From: "service/s.go::Process", To: "service/u.go::Util", Kind: graph.EdgeCalls})

	out := callCouplingHandler(t, s, map[string]any{"package_depth": 1})
	svc := findCouplingRow(out, "service")
	require.NotNil(t, svc)
	// Internal-edge count went up; ca/ce should NOT have changed.
	assert.EqualValues(t, 1, svc["internal_edges"].(float64), "intra-service call counted as internal")
	assert.EqualValues(t, 1, svc["ca"].(float64))
	assert.EqualValues(t, 1, svc["ce"].(float64))
}

func TestCoupling_CommunityRollupHonored(t *testing.T) {
	s := newCouplingTestServer(t)
	s.analysisMu.Lock()
	s.communities = &analysis.CommunityResult{
		NodeToComm: map[string]string{
			"api/h.go::Handle":      "c-edge",
			"service/s.go::Process": "c-core",
			"repo/r.go::Fetch":      "c-core",
		},
		Communities: []analysis.Community{
			{ID: "c-edge", Members: []string{"api/h.go::Handle"}},
			{ID: "c-core", Members: []string{"service/s.go::Process", "repo/r.go::Fetch"}},
		},
	}
	s.analysisMu.Unlock()

	out := callCouplingHandler(t, s, map[string]any{"unit": "community"})

	edge := findCouplingRow(out, "c-edge")
	core := findCouplingRow(out, "c-core")
	require.NotNil(t, edge)
	require.NotNil(t, core)

	// c-edge depends on c-core; ca=0, ce=1.
	assert.EqualValues(t, 1, edge["ce"].(float64))
	assert.EqualValues(t, 0, edge["ca"].(float64))
	// c-core: 1 incoming (api → service), internal service → repo.
	assert.EqualValues(t, 1, core["ca"].(float64))
	assert.EqualValues(t, 0, core["ce"].(float64))
	assert.EqualValues(t, 1, core["internal_edges"].(float64))
}

func TestCoupling_PackageDepthControlsGrouping(t *testing.T) {
	s := newCouplingTestServer(t)
	// At depth=2 we should see the same packages (only 1 segment each).
	// Add a deeper file to test depth=2 grouping.
	s.graph.AddNode(&graph.Node{ID: "service/internal/x.go::X", Name: "X", Kind: graph.KindFunction, FilePath: "service/internal/x.go"})
	s.graph.AddEdge(&graph.Edge{From: "service/internal/x.go::X", To: "repo/r.go::Fetch", Kind: graph.EdgeCalls})

	out := callCouplingHandler(t, s, map[string]any{"package_depth": 2})
	deep := findCouplingRow(out, "service/internal")
	require.NotNil(t, deep, "package_depth=2 should produce service/internal as a distinct unit")
	assert.EqualValues(t, 1, deep["ce"].(float64))
}

func TestCoupling_SortByOptions(t *testing.T) {
	s := newCouplingTestServer(t)
	for _, sortBy := range []string{"ca", "ce", "instability", "members", "ca_and_instability"} {
		out := callCouplingHandler(t, s, map[string]any{"sort_by": sortBy, "package_depth": 1})
		assert.Equal(t, sortBy, out["sort_by"])
		rows, _ := out["units"].([]any)
		assert.NotEmpty(t, rows)
	}
}

func TestCoupling_MinMembersFilter(t *testing.T) {
	s := newCouplingTestServer(t)
	out := callCouplingHandler(t, s, map[string]any{
		"package_depth": 1,
		"min_members":   5, // every unit has 1 member — all dropped.
	})
	rows, _ := out["units"].([]any)
	assert.Empty(t, rows)
}

func TestCoupling_PathPrefixScope(t *testing.T) {
	s := newCouplingTestServer(t)
	out := callCouplingHandler(t, s, map[string]any{
		"package_depth": 1,
		"path_prefix":   "service/",
	})
	rows, _ := out["units"].([]any)
	// Only service in scope; the cross-unit edges to api / repo are
	// dropped because their endpoints are out of scope.
	require.Len(t, rows, 1)
	assert.Equal(t, "service", rows[0].(map[string]any)["unit"])
}

func TestPackageOfPath(t *testing.T) {
	cases := []struct {
		path  string
		depth int
		want  string
	}{
		{"a/b/c.go", 2, "a/b"},
		{"a/b/c.go", 1, "a"},
		{"a/b/c/d.go", 2, "a/b"},
		{"/abs/a/b/c.go", 2, "abs/a"},
		{"justfile.go", 1, ""}, // no directory parts
		{"", 2, ""},
		{"a/b", 2, "a/b"}, // no .go extension — keep as-is
	}
	for _, c := range cases {
		got := packageOfPath(c.path, c.depth)
		assert.Equal(t, c.want, got, "packageOfPath(%q, %d)", c.path, c.depth)
	}
}

func TestCoupling_LimitTruncates(t *testing.T) {
	s := newCouplingTestServer(t)
	out := callCouplingHandler(t, s, map[string]any{
		"package_depth": 1,
		"limit":         1,
	})
	rows, _ := out["units"].([]any)
	assert.Len(t, rows, 1)
	assert.Equal(t, true, out["truncated"])
}
