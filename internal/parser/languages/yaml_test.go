package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

func TestYAMLExtractor_TopLevelKeys(t *testing.T) {
	src := []byte(`name: my-app
version: "1.0"
services:
  web:
    image: nginx
  db:
    image: postgres
`)
	e := NewYAMLExtractor()
	result, err := e.Extract("docker-compose.yml", src)
	require.NoError(t, err)

	vars := nodesOfKind(result.Nodes, graph.KindVariable)
	assert.GreaterOrEqual(t, len(vars), 3, "should extract at least name, version, services")

	defines := edgesOfKind(result.Edges, graph.EdgeDefines)
	assert.GreaterOrEqual(t, len(defines), 3)
}

func TestYAMLExtractor_SimpleMapping(t *testing.T) {
	src := []byte(`database:
  host: localhost
  port: 5432
logging:
  level: info
`)
	e := NewYAMLExtractor()
	result, err := e.Extract("config.yaml", src)
	require.NoError(t, err)

	vars := nodesOfKind(result.Nodes, graph.KindVariable)
	names := make(map[string]bool)
	for _, v := range vars {
		names[v.Name] = true
	}
	assert.True(t, names["database"], "should extract 'database' key")
	assert.True(t, names["logging"], "should extract 'logging' key")
}

func TestYAMLExtractor_BlockSequenceScalars(t *testing.T) {
	// Verify that scalar items in a block_sequence are extracted.
	src := []byte(`branches:
  - main
  - development
  - qas
`)
	e := NewYAMLExtractor()
	result, err := e.Extract("config.yml", src)
	require.NoError(t, err)

	vars := nodesOfKind(result.Nodes, graph.KindVariable)
	names := make(map[string]bool)
	for _, v := range vars {
		names[v.Name] = true
	}
	assert.True(t, names["branches"], "should extract 'branches' key")
	assert.True(t, names["main"], "should extract 'main' from sequence")
	assert.True(t, names["development"], "should extract 'development' from sequence")
	assert.True(t, names["qas"], "should extract 'qas' from sequence")
}

func TestYAMLExtractor_BlockSequenceMappings(t *testing.T) {
	// Verify that mapping items inside a block_sequence are extracted
	// as nodes with metadata (service-map style).
	src := []byte(`services:
  - name: ave-ai-vault
    repo: https://github.com/example/vault.git
  - name: ave-auth
    repo: https://github.com/example/auth.git
`)
	e := NewYAMLExtractor()
	result, err := e.Extract("services.yml", src)
	require.NoError(t, err)

	// Should have: services (top-level) + 2 item nodes
	itemIDs := make(map[string]bool)
	for _, n := range result.Nodes {
		if n.Name == "ave-ai-vault" || n.Name == "ave-auth" {
			itemIDs[n.ID] = true
			// Check metadata
			if n.Meta == nil {
				t.Errorf("item %s should have metadata", n.Name)
				continue
			}
			repoURL, ok := n.Meta["repo_url"].(string)
			if !ok || repoURL == "" {
				t.Errorf("item %s should have repo_url in metadata", n.Name)
			}
			parentKey, ok := n.Meta["parent_key"].(string)
			if !ok || parentKey != "services" {
				t.Errorf("item %s parent_key = %v, want services", n.Name, n.Meta["parent_key"])
			}
		}
	}
	assert.Equal(t, 2, len(itemIDs), "should have 2 named service items")

	// Verify defines edges exist.
	defines := edgesOfKind(result.Edges, graph.EdgeDefines)
	assert.GreaterOrEqual(t, len(defines), 3, "at least services + 2 items")
}

func TestYAMLExtractor_DeterministicNodeIDs(t *testing.T) {
	src := []byte(`services:
  - name: ave-api
    repo: https://example.com/api.git
`)
	// Extract twice — IDs must match.
	e := NewYAMLExtractor()
	r1, _ := e.Extract("test.yml", src)
	r2, _ := e.Extract("test.yml", src)

	idSet1 := make(map[string]bool)
	for _, n := range r1.Nodes {
		idSet1[n.ID] = true
	}
	for _, n := range r2.Nodes {
		if !idSet1[n.ID] {
			t.Errorf("node ID %q present in first extract but not second", n.ID)
		}
	}
}

func TestYAMLExtractor_ExistingTestsStillPass(t *testing.T) {
	// Verify the existing test cases still produce the expected output
	// after the block_sequence enhancement.
	src := []byte(`name: my-app
version: "1.0"
services:
  web:
    image: nginx
`)
	e := NewYAMLExtractor()
	result, err := e.Extract("docker-compose.yml", src)
	require.NoError(t, err)

	vars := nodesOfKind(result.Nodes, graph.KindVariable)
	names := make(map[string]bool)
	for _, v := range vars {
		names[v.Name] = true
	}
	assert.True(t, names["name"], "should extract 'name'")
	assert.True(t, names["version"], "should extract 'version'")
	assert.True(t, names["services"], "should extract 'services'")
	// "web" is nested (not top-level) so it should NOT appear as a
	// top-level variable — but it may appear as a sequence item.
}

func TestYAMLExtractor_FileNode(t *testing.T) {
	src := []byte(`key: value
`)
	e := NewYAMLExtractor()
	result, err := e.Extract("test.yml", src)
	require.NoError(t, err)

	files := nodesOfKind(result.Nodes, graph.KindFile)
	assert.Equal(t, 1, len(files))
	assert.Equal(t, "test.yml", files[0].Name)
}
