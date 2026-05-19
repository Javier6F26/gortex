package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/analysis"
	"github.com/zzet/gortex/internal/graph"
)

func newWakeupTestServer(t *testing.T) *Server {
	t.Helper()
	g := graph.New()
	g.AddNode(&graph.Node{ID: "p/main.go", Name: "main.go", Kind: graph.KindFile, FilePath: "p/main.go"})
	g.AddNode(&graph.Node{ID: "p/util.go", Name: "util.go", Kind: graph.KindFile, FilePath: "p/util.go"})
	g.AddNode(&graph.Node{ID: "p/main.go::Run", Name: "Run", Kind: graph.KindFunction, FilePath: "p/main.go", Language: "go"})
	g.AddNode(&graph.Node{ID: "p/util.go::Helper", Name: "Helper", Kind: graph.KindFunction, FilePath: "p/util.go", Language: "go"})
	g.AddNode(&graph.Node{ID: "p/scripts.py::main", Name: "main", Kind: graph.KindFunction, FilePath: "p/scripts.py", Language: "python"})
	g.AddEdge(&graph.Edge{From: "p/main.go::Run", To: "p/util.go::Helper", Kind: graph.EdgeCalls})

	s := &Server{
		graph:      g,
		session:    newSessionState(),
		tokenStats: &tokenStats{},
		symHistory: &symbolHistory{entries: make(map[string][]SymbolModification)},
		sessions:   newSessionMap(),
		toolScopes: newScopeRegistry(),
	}
	s.analysisMu.Lock()
	s.communities = &analysis.CommunityResult{
		Communities: []analysis.Community{
			{ID: "c1", Label: "core", Size: 3, Hub: "Run", Members: []string{"p/main.go::Run", "p/util.go::Helper"}},
		},
	}
	s.analysisMu.Unlock()
	return s
}

func callWakeupHandler(t *testing.T, s *Server, args map[string]any) (text string, jsonOut map[string]any) {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	res, err := s.handleGortexWakeup(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.False(t, res.IsError)
	tc, ok := res.Content[0].(mcp.TextContent)
	require.True(t, ok)
	text = tc.Text
	if strings.HasPrefix(text, "{") {
		_ = json.Unmarshal([]byte(text), &jsonOut)
	}
	return text, jsonOut
}

func TestWakeup_MarkdownDefault(t *testing.T) {
	s := newWakeupTestServer(t)
	text, _ := callWakeupHandler(t, s, map[string]any{})

	assert.Contains(t, text, "# Codebase wakeup")
	assert.Contains(t, text, "**Scale.**")
	assert.Contains(t, text, "go")
	assert.NotContains(t, text, "{", "default format is markdown, not JSON")
}

func TestWakeup_JSONEnvelope(t *testing.T) {
	s := newWakeupTestServer(t)
	_, jsonOut := callWakeupHandler(t, s, map[string]any{"format": "json"})

	require.NotNil(t, jsonOut)
	md, _ := jsonOut["markdown"].(string)
	assert.Contains(t, md, "Codebase wakeup")
	assert.Greater(t, jsonOut["tokens_est"].(float64), 0.0)
}

func TestWakeup_CommunitiesSurface(t *testing.T) {
	s := newWakeupTestServer(t)
	text, _ := callWakeupHandler(t, s, map[string]any{})
	assert.Contains(t, text, "**Communities.**")
	assert.Contains(t, text, "core")
	assert.Contains(t, text, "hub Run")
}

func TestWakeup_HotspotsHonoredWhenPresent(t *testing.T) {
	// FindHotspots gates on mean+2σ — on a 5-node graph nothing
	// usually qualifies, so we build a larger graph here to force
	// at least one hotspot through the gate. Confirms the section
	// renders when hotspots exist; absence is silent (no empty
	// header) — preserve that property.
	g := graph.New()
	g.AddNode(&graph.Node{ID: "hub", Name: "hub", Kind: graph.KindFunction, FilePath: "h.go", Language: "go"})
	for i := range 30 {
		id := "c" + string(rune('A'+i%26)) + string(rune('0'+(i/26)))
		g.AddNode(&graph.Node{ID: id, Name: id, Kind: graph.KindFunction, FilePath: id + ".go", Language: "go"})
		g.AddEdge(&graph.Edge{From: id, To: "hub", Kind: graph.EdgeCalls})
	}
	s := &Server{
		graph:      g,
		session:    newSessionState(),
		tokenStats: &tokenStats{},
		symHistory: &symbolHistory{entries: make(map[string][]SymbolModification)},
		sessions:   newSessionMap(),
		toolScopes: newScopeRegistry(),
	}
	text, _ := callWakeupHandler(t, s, map[string]any{})
	assert.Contains(t, text, "**Load-bearing symbols.**")
	assert.Contains(t, text, "hub")
}

func TestWakeup_EntryPointsSurface(t *testing.T) {
	s := newWakeupTestServer(t)
	text, _ := callWakeupHandler(t, s, map[string]any{})
	// Run has zero in-edges and one out-edge — qualifies as entry point.
	assert.Contains(t, text, "**Entry points.**")
	assert.Contains(t, text, "Run")
}

func TestWakeup_TokenBudgetCaps(t *testing.T) {
	s := newWakeupTestServer(t)
	text, _ := callWakeupHandler(t, s, map[string]any{"max_tokens": 20})
	// At 20 tokens (~80 chars), output truncates with the marker.
	assert.LessOrEqual(t, len(text), 200, "small budget produces small output")
	assert.Contains(t, text, "truncated", "truncation marker present")
}

func TestWakeup_TopCommunitiesCap(t *testing.T) {
	s := newWakeupTestServer(t)
	s.analysisMu.Lock()
	s.communities = &analysis.CommunityResult{
		Communities: []analysis.Community{
			{ID: "c1", Label: "a", Size: 5},
			{ID: "c2", Label: "b", Size: 4},
			{ID: "c3", Label: "c", Size: 3},
			{ID: "c4", Label: "d", Size: 2},
			{ID: "c5", Label: "e", Size: 1},
		},
	}
	s.analysisMu.Unlock()

	text, _ := callWakeupHandler(t, s, map[string]any{"top_communities": 2})
	assert.Contains(t, text, "a (5")
	assert.Contains(t, text, "b (4")
	assert.NotContains(t, text, "c (3", "c onwards is dropped under top_communities=2")
}

func TestWakeup_DefaultOptions(t *testing.T) {
	o := DefaultWakeupOptions()
	assert.Equal(t, 500, o.MaxTokens)
	assert.Equal(t, 4, o.TopCommunities)
	assert.Equal(t, 5, o.TopHotspots)
	assert.Equal(t, 5, o.TopEntryPoints)
}

func TestTrimToTokens(t *testing.T) {
	// Short content: returned unchanged.
	short := "hello world"
	assert.Equal(t, short, trimToTokens(short, 100))

	// Long content: cut at line boundary with marker.
	long := strings.Repeat("xxxxxxxx\n", 200)
	out := trimToTokens(long, 50)
	assert.Less(t, len(out), len(long))
	assert.Contains(t, out, "truncated")
}

func TestBuildWakeupDirect_ExposedForCLI(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{ID: "x.go", Name: "x.go", Kind: graph.KindFile, FilePath: "x.go"})
	g.AddNode(&graph.Node{ID: "x.go::X", Name: "X", Kind: graph.KindFunction, FilePath: "x.go", Language: "go"})

	md, est := BuildWakeup(g, nil, DefaultWakeupOptions())
	assert.NotEmpty(t, md)
	assert.Contains(t, md, "Codebase wakeup")
	assert.Greater(t, est, 0)
}
