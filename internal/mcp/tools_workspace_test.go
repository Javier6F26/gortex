package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/query"
)

// An unbound daemon serving a multi-repo store lists the repo prefixes
// present in the graph instead of returning an empty unbound envelope.
func TestListRepos_UnboundEnumeratesGraphRepos(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{
		ID: "repoA/a.go::A", Kind: graph.KindFunction, Name: "A",
		FilePath: "repoA/a.go", RepoPrefix: "repoA",
	})
	g.AddNode(&graph.Node{
		ID: "repoB/b.go::B", Kind: graph.KindFunction, Name: "B",
		FilePath: "repoB/b.go", RepoPrefix: "repoB",
	})

	eng := query.NewEngine(g)
	srv := NewServer(eng, g, nil, nil, zap.NewNop(), nil)

	payload := srv.buildListReposPayload(context.Background())
	require.Equal(t, "unbound", payload["mode"])

	repos, ok := payload["repos"].([]map[string]any)
	require.True(t, ok, "repos must be a list of entries, got %T", payload["repos"])
	names := make([]string, 0, len(repos))
	for _, r := range repos {
		names = append(names, r["name"].(string))
	}
	require.ElementsMatch(t, []string{"repoA", "repoB"}, names)
}

// workspace_info must enumerate the same repo set as list_repos on an
// unbound daemon (a follower). Before the fix it returned an empty repos
// array while list_repos served the graph's prefixes (4.1).
func TestWorkspaceInfo_UnboundAgreesWithListRepos(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{
		ID: "repoA/a.go::A", Kind: graph.KindFunction, Name: "A",
		FilePath: "repoA/a.go", RepoPrefix: "repoA",
	})
	g.AddNode(&graph.Node{
		ID: "repoB/b.go::B", Kind: graph.KindFunction, Name: "B",
		FilePath: "repoB/b.go", RepoPrefix: "repoB",
	})

	eng := query.NewEngine(g)
	srv := NewServer(eng, g, nil, nil, zap.NewNop(), nil)

	ws := srv.buildWorkspaceInfoPayload(context.Background())
	require.Equal(t, "unbound", ws["mode"])
	wsRepos, ok := ws["repos"].([]map[string]any)
	require.True(t, ok, "workspace_info repos must be entries, got %T", ws["repos"])

	lr := srv.buildListReposPayload(context.Background())
	lrRepos := lr["repos"].([]map[string]any)

	nameSet := func(entries []map[string]any) []string {
		out := make([]string, 0, len(entries))
		for _, e := range entries {
			out = append(out, e["name"].(string))
		}
		return out
	}
	require.ElementsMatch(t, nameSet(lrRepos), nameSet(wsRepos),
		"workspace_info and list_repos must report the same repo set")
	require.ElementsMatch(t, []string{"repoA", "repoB"}, nameSet(wsRepos))
}

// get_active_project on an unbound daemon with no config-defined repos
// (a follower) must fall back to the graph's repo set instead of an empty
// list, so it agrees with list_repos / workspace_info (4.7).
func TestGetActiveProject_UnboundFallsBackToGraphRepos(t *testing.T) {
	g := graph.New()
	g.AddNode(&graph.Node{
		ID: "repoA/a.go::A", Kind: graph.KindFunction, Name: "A",
		FilePath: "repoA/a.go", RepoPrefix: "repoA",
	})
	g.AddNode(&graph.Node{
		ID: "repoB/b.go::B", Kind: graph.KindFunction, Name: "B",
		FilePath: "repoB/b.go", RepoPrefix: "repoB",
	})
	eng := query.NewEngine(g)
	srv := NewServer(eng, g, nil, nil, zap.NewNop(), nil) // configManager nil

	payload := srv.buildActiveProjectPayload(context.Background())
	repos, ok := payload["repos"].([]map[string]any)
	require.True(t, ok, "unbound repos must fall back to graph entries, got %T", payload["repos"])
	names := make([]string, 0, len(repos))
	for _, r := range repos {
		names = append(names, r["name"].(string))
	}
	require.ElementsMatch(t, []string{"repoA", "repoB"}, names)
}
