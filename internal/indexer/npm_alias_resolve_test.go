package indexer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/resolver"
)

// writePackageJSON writes a package.json into dir, creating any
// missing parent directories. Returns dir for convenient chaining.
func writePackageJSON(t *testing.T, dir, body string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte(body), 0o644))
	return dir
}

func TestSplitPackageSpecifier(t *testing.T) {
	cases := []struct {
		in           string
		pkg, subPath string
	}{
		{"shared", "shared", ""},
		{"shared/util", "shared", "util"},
		{"shared/util/deep", "shared", "util/deep"},
		{"@acme/lib", "@acme/lib", ""},
		{"@acme/lib/util", "@acme/lib", "util"},
		{"@acme/lib/util/deep", "@acme/lib", "util/deep"},
		{"@bad", "", ""}, // scope with no name
	}
	for _, c := range cases {
		pkg, sub := splitPackageSpecifier(c.in)
		if pkg != c.pkg || sub != c.subPath {
			t.Errorf("splitPackageSpecifier(%q) = (%q, %q), want (%q, %q)",
				c.in, pkg, sub, c.pkg, c.subPath)
		}
	}
}

// TestNpmAliasIndex_Resolve drives the disk-backed resolver over a
// real monorepo layout: a workspace-root package.json plus a nested
// per-package one, both holding npm-alias dependency entries.
func TestNpmAliasIndex_Resolve(t *testing.T) {
	root := t.TempDir()

	// Workspace-root package.json — declares one alias.
	writePackageJSON(t, root, `{
  "name": "monorepo-root",
  "dependencies": {
    "rootdep": "npm:@acme/root-lib@2.0.0"
  }
}`)
	// Nested package package.json — the nearest-ancestor manifest for
	// files under packages/app. Covers scoped+version, plain+version,
	// no-version, a dev-dependency alias, and an ordinary dep.
	writePackageJSON(t, filepath.Join(root, "packages", "app"), `{
  "name": "@acme/app",
  "dependencies": {
    "shared": "npm:@acme/shared-lib@1.4.0",
    "lodash4": "npm:lodash@4.17.21",
    "nover": "npm:@acme/no-version",
    "react": "^18.2.0"
  },
  "devDependencies": {
    "test-utils": "npm:@acme/test-utils@0.1.0"
  }
}`)

	idx := newNpmAliasIndex(map[string]string{"": root})
	require.NotNil(t, idx)

	caller := "packages/app/src/components/Widget.ts"
	cases := []struct {
		name      string
		callerRel string
		specifier string
		want      string
	}{
		{"scoped alias with version", caller, "shared", "@acme/shared-lib"},
		{"plain alias with version", caller, "lodash4", "lodash"},
		{"alias without version", caller, "nover", "@acme/no-version"},
		{"alias sub-path import", caller, "shared/util", "@acme/shared-lib/util"},
		{"alias deep sub-path", caller, "shared/util/deep", "@acme/shared-lib/util/deep"},
		{"dev-dependency alias", caller, "test-utils", "@acme/test-utils"},
		{"ordinary dep is not rewritten", caller, "react", ""},
		{"unknown specifier is not rewritten", caller, "express", ""},
		{"relative import is never an alias", caller, "./local", ""},
		{"non-JS/TS caller is skipped", "packages/app/main.go", "shared", ""},
		// A file directly under the workspace root sees only the root
		// manifest — `shared` belongs to the nested package, not here.
		{"nearest-ancestor scoping (root file)", "index.ts", "shared", ""},
		{"root manifest alias resolves at root", "index.ts", "rootdep", "@acme/root-lib"},
		// The nested file falls through to the root manifest for an
		// alias the nested package.json does not declare.
		{"ancestor walk reaches the root manifest", caller, "rootdep", "@acme/root-lib"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := idx.Resolve(c.callerRel, c.specifier)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestNpmAliasIndex_NilRootsYieldsNil(t *testing.T) {
	assert.Nil(t, newNpmAliasIndex(nil))
	assert.Nil(t, newNpmAliasIndex(map[string]string{"repo": ""}))
}

// addPackageNode registers a KindPackage node with the given qualified
// name — this is what CrossRepoResolver.resolveImport matches an
// import path against (mirrors the existing cross-repo import tests).
func addPackageNode(g *graph.Graph, repo, file, qualName string) {
	g.AddNode(&graph.Node{
		ID: file, Kind: graph.KindPackage, Name: qualName, QualName: qualName,
		FilePath: file, Language: "typescript", RepoPrefix: repo,
	})
}

// TestNpmAliasImportResolution is the end-to-end check: an import edge
// whose specifier is an npm-alias key resolves to the locally-indexed
// real package, including the sub-path and no-version forms; an alias
// whose real package is NOT indexed still resolves to an external
// stub (no regression).
func TestNpmAliasImportResolution(t *testing.T) {
	root := t.TempDir()
	writePackageJSON(t, filepath.Join(root, "packages", "app"), `{
  "name": "@acme/app",
  "dependencies": {
    "shared": "npm:@acme/shared-lib@1.4.0",
    "nover": "npm:@acme/no-version",
    "missing": "npm:@acme/not-indexed@9.9.9"
  }
}`)

	aliasIdx := newNpmAliasIndex(map[string]string{"": root})
	require.NotNil(t, aliasIdx)

	cases := []struct {
		name      string
		specifier string
		wantTo    string
		wantStats func(t *testing.T, s *resolver.CrossRepoStats)
	}{
		{
			name:      "alias key resolves to the locally-indexed real package",
			specifier: "shared",
			wantTo:    "packages/shared-lib/src/index.ts",
			wantStats: func(t *testing.T, s *resolver.CrossRepoStats) {
				assert.Equal(t, 1, s.Resolved)
			},
		},
		{
			name:      "alias sub-path import resolves to the real package",
			specifier: "shared/util",
			wantTo:    "packages/shared-lib/src/index.ts",
			wantStats: func(t *testing.T, s *resolver.CrossRepoStats) {
				assert.Equal(t, 1, s.Resolved)
			},
		},
		{
			name:      "alias without a version resolves to the real package",
			specifier: "nover",
			wantTo:    "packages/no-version/src/index.ts",
			wantStats: func(t *testing.T, s *resolver.CrossRepoStats) {
				assert.Equal(t, 1, s.Resolved)
			},
		},
		{
			// Negative case: the alias real package is not indexed —
			// resolution falls through to an external stub exactly as a
			// plain unindexed import would. No regression.
			name:      "alias whose real package is not indexed stays external",
			specifier: "missing",
			wantTo:    "external::@acme/not-indexed",
			wantStats: func(t *testing.T, s *resolver.CrossRepoStats) {
				assert.Equal(t, 0, s.Resolved)
				assert.Equal(t, 1, s.Unresolved)
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := graph.New()
			caller := "packages/app/src/main.ts"
			g.AddNode(&graph.Node{
				ID: caller, Kind: graph.KindFile, Name: "main.ts",
				FilePath: caller, Language: "typescript",
			})
			// Locally-vendored real packages, keyed by their npm name.
			addPackageNode(g, "", "packages/shared-lib/src/index.ts", "@acme/shared-lib")
			addPackageNode(g, "", "packages/no-version/src/index.ts", "@acme/no-version")

			edge := &graph.Edge{
				From: caller, To: "unresolved::import::" + c.specifier,
				Kind: graph.EdgeImports, FilePath: caller, Line: 1,
			}
			g.AddEdge(edge)

			cr := resolver.NewCrossRepo(g)
			cr.SetNpmAliasResolver(aliasIdx.Resolve)
			stats := cr.ResolveAll()

			assert.Equal(t, c.wantTo, edge.To)
			c.wantStats(t, stats)
		})
	}
}

// TestNpmAliasImportResolution_NoResolverIsExternal pins the no-regression
// baseline: without the alias resolver installed, an import of an alias
// key resolves to an external stub under the bare specifier — exactly
// the pre-feature behaviour.
func TestNpmAliasImportResolution_NoResolverIsExternal(t *testing.T) {
	g := graph.New()
	caller := "packages/app/src/main.ts"
	g.AddNode(&graph.Node{
		ID: caller, Kind: graph.KindFile, Name: "main.ts",
		FilePath: caller, Language: "typescript",
	})
	addPackageNode(g, "", "packages/shared-lib/src/index.ts", "@acme/shared-lib")

	edge := &graph.Edge{
		From: caller, To: "unresolved::import::shared",
		Kind: graph.EdgeImports, FilePath: caller, Line: 1,
	}
	g.AddEdge(edge)

	cr := resolver.NewCrossRepo(g) // no SetNpmAliasResolver
	cr.ResolveAll()

	require.True(t, strings.HasPrefix(edge.To, "external::"),
		"without the alias resolver the bare specifier must stay external, got %q", edge.To)
	assert.Equal(t, "external::shared", edge.To)
}
