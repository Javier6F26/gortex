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
