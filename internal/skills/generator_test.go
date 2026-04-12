package skills

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/analysis"
	"github.com/zzet/gortex/internal/graph"
)

func testGraph() (*graph.Graph, *analysis.CommunityResult, *analysis.ProcessResult) {
	g := graph.New()

	// Parser community.
	g.AddNode(&graph.Node{ID: "parser/parse.go::Parse", Kind: graph.KindFunction, Name: "Parse", FilePath: "parser/parse.go", Language: "go"})
	g.AddNode(&graph.Node{ID: "parser/parse.go::Tokenize", Kind: graph.KindFunction, Name: "Tokenize", FilePath: "parser/parse.go", Language: "go"})
	g.AddNode(&graph.Node{ID: "parser/ast.go::BuildAST", Kind: graph.KindFunction, Name: "BuildAST", FilePath: "parser/ast.go", Language: "go"})
	g.AddNode(&graph.Node{ID: "parser/ast.go::Node", Kind: graph.KindType, Name: "Node", FilePath: "parser/ast.go", Language: "go"})

	// Server community.
	g.AddNode(&graph.Node{ID: "server/handler.go::HandleRequest", Kind: graph.KindFunction, Name: "HandleRequest", FilePath: "server/handler.go", Language: "go"})
	g.AddNode(&graph.Node{ID: "server/handler.go::Middleware", Kind: graph.KindFunction, Name: "Middleware", FilePath: "server/handler.go", Language: "go"})
	g.AddNode(&graph.Node{ID: "server/router.go::Route", Kind: graph.KindFunction, Name: "Route", FilePath: "server/router.go", Language: "go"})

	// Tiny community (should be filtered).
	g.AddNode(&graph.Node{ID: "util/helper.go::Max", Kind: graph.KindFunction, Name: "Max", FilePath: "util/helper.go", Language: "go"})

	// Cross-community edge.
	g.AddEdge(&graph.Edge{From: "server/handler.go::HandleRequest", To: "parser/parse.go::Parse", Kind: graph.EdgeCalls})

	communities := &analysis.CommunityResult{
		Communities: []analysis.Community{
			{
				ID: "community-0", Label: "parser", Size: 4, Cohesion: 0.8,
				Members: []string{"parser/parse.go::Parse", "parser/parse.go::Tokenize", "parser/ast.go::BuildAST", "parser/ast.go::Node"},
				Files:   []string{"parser/parse.go", "parser/ast.go"},
			},
			{
				ID: "community-1", Label: "server", Size: 3, Cohesion: 0.65,
				Members: []string{"server/handler.go::HandleRequest", "server/handler.go::Middleware", "server/router.go::Route"},
				Files:   []string{"server/handler.go", "server/router.go"},
			},
			{
				ID: "community-2", Label: "util", Size: 1, Cohesion: 1.0,
				Members: []string{"util/helper.go::Max"},
				Files:   []string{"util/helper.go"},
			},
		},
		NodeToComm: map[string]string{
			"parser/parse.go::Parse":           "community-0",
			"parser/parse.go::Tokenize":        "community-0",
			"parser/ast.go::BuildAST":          "community-0",
			"parser/ast.go::Node":              "community-0",
			"server/handler.go::HandleRequest": "community-1",
			"server/handler.go::Middleware":     "community-1",
			"server/router.go::Route":          "community-1",
			"util/helper.go::Max":              "community-2",
		},
		Modularity: 0.45,
	}

	processes := &analysis.ProcessResult{
		Processes: []analysis.Process{
			{
				ID: "proc-0", Name: "request-handling", EntryPoint: "server/handler.go::HandleRequest",
				Steps: []string{"server/handler.go::HandleRequest", "parser/parse.go::Parse"},
				Files: []string{"server/handler.go", "parser/parse.go"},
			},
		},
	}

	return g, communities, processes
}

func TestGenerateAll_BasicCommunities(t *testing.T) {
	g, communities, processes := testGraph()
	gen := New(communities, processes, g)

	skills := gen.GenerateAll()
	require.Len(t, skills, 2, "should generate 2 skills (util filtered out)")

	// Sorted by size descending: parser (4) then server (3).
	assert.Equal(t, "parser", skills[0].Label)
	assert.Equal(t, "gortex-parser", skills[0].DirName)
	assert.Equal(t, "server", skills[1].Label)

	// Verify parser skill content.
	assert.Contains(t, skills[0].Content, "name: gortex-parser")
	assert.Contains(t, skills[0].Content, "4 symbols")
	assert.Contains(t, skills[0].Content, "2 files")
	assert.Contains(t, skills[0].Content, "80% cohesion")
	assert.Contains(t, skills[0].Content, "parser/parse.go")
	assert.Contains(t, skills[0].Content, "parser/ast.go")
	assert.Contains(t, skills[0].Content, "Parse, Tokenize")

	// Verify server skill has entry point.
	assert.Contains(t, skills[1].Content, "server/handler.go::HandleRequest")
	assert.Contains(t, skills[1].Content, "## Entry Points")
}

func TestGenerateAll_FilterSmallCommunities(t *testing.T) {
	g, communities, processes := testGraph()
	gen := New(communities, processes, g)

	skills := gen.GenerateAll()

	// util community has size 1 → filtered out.
	for _, s := range skills {
		assert.NotEqual(t, "util", s.Label, "small communities should be excluded")
	}
}

func TestGenerateAll_CrossCommunityConnections(t *testing.T) {
	g, communities, processes := testGraph()
	gen := New(communities, processes, g)

	skills := gen.GenerateAll()

	// Server calls parser → server skill should mention parser connection.
	var serverSkill *GeneratedSkill
	for i := range skills {
		if skills[i].Label == "server" {
			serverSkill = &skills[i]
			break
		}
	}
	require.NotNil(t, serverSkill)
	assert.Contains(t, serverSkill.Content, "## Connected Communities")
	assert.Contains(t, serverSkill.Content, "parser")
}

func TestGenerateRouting(t *testing.T) {
	g, communities, processes := testGraph()
	gen := New(communities, processes, g)

	skills := gen.GenerateAll()
	routing := gen.GenerateRouting(skills)

	assert.Contains(t, routing, "<!-- gortex:skills:start -->")
	assert.Contains(t, routing, "<!-- gortex:skills:end -->")
	assert.Contains(t, routing, "| Parser |")
	assert.Contains(t, routing, "| Server |")
	assert.Contains(t, routing, "`/gortex-parser`")
	assert.Contains(t, routing, "`/gortex-server`")
}

func TestGenerateAll_NilCommunities(t *testing.T) {
	g := graph.New()
	gen := New(nil, nil, g)
	skills := gen.GenerateAll()
	assert.Nil(t, skills)
}

func TestToKebab(t *testing.T) {
	assert.Equal(t, "mcp-server", toKebab("MCP Server"))
	assert.Equal(t, "parser", toKebab("parser"))
	assert.Equal(t, "internal-graph", toKebab("internal/graph"))
	assert.Equal(t, "foo-bar-baz", toKebab("Foo  Bar--Baz"))
}

func TestGenerateAll_HowToExplore(t *testing.T) {
	g, communities, processes := testGraph()
	gen := New(communities, processes, g)

	skills := gen.GenerateAll()
	for _, s := range skills {
		assert.True(t, strings.Contains(s.Content, "## How to Explore"), "skill %s should have How to Explore section", s.Label)
		assert.True(t, strings.Contains(s.Content, "get_communities"), "skill %s should reference get_communities tool", s.Label)
		assert.True(t, strings.Contains(s.Content, "smart_context"), "skill %s should reference smart_context tool", s.Label)
	}
}
