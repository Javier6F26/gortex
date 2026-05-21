package modules

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func TestParsePackageJSONWorkspaces(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "array form",
			src:  `{"name":"root","workspaces":["packages/*","apps/web"]}`,
			want: []string{"packages/*", "apps/web"},
		},
		{
			name: "object form with packages",
			src:  `{"name":"root","workspaces":{"packages":["libs/*"],"nohoist":["**/react"]}}`,
			want: []string{"libs/*"},
		},
		{
			name: "no workspaces field",
			src:  `{"name":"plain","version":"1.0.0","dependencies":{"react":"^18"}}`,
			want: nil,
		},
		{
			name: "empty array",
			src:  `{"name":"root","workspaces":[]}`,
			want: nil,
		},
		{
			name: "whitespace-only entries dropped",
			src:  `{"workspaces":["packages/*","  "]}`,
			want: []string{"packages/*"},
		},
		{
			name: "malformed json",
			src:  `not json`,
			want: nil,
		},
		{
			name: "empty input",
			src:  ``,
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParsePackageJSONWorkspaces([]byte(c.src))
			if !equalStrings(got, c.want) {
				t.Errorf("ParsePackageJSONWorkspaces() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestParsePnpmWorkspaceYAML(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "block sequence",
			src:  "packages:\n  - 'packages/*'\n  - \"apps/*\"\n  - tools/cli\n",
			want: []string{"packages/*", "apps/*", "tools/cli"},
		},
		{
			name: "flow sequence inline",
			src:  "packages: ['packages/*', \"libs/*\"]\n",
			want: []string{"packages/*", "libs/*"},
		},
		{
			name: "comments and blank lines skipped",
			src:  "# pnpm workspace\n\npackages:\n  # members\n  - 'packages/*'\n",
			want: []string{"packages/*"},
		},
		{
			name: "section ends at next top-level key",
			src:  "packages:\n  - 'packages/*'\ncatalog:\n  react: ^18\n",
			want: []string{"packages/*"},
		},
		{
			name: "no packages key",
			src:  "catalog:\n  react: ^18\n",
			want: nil,
		},
		{
			name: "empty input",
			src:  ``,
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParsePnpmWorkspaceYAML([]byte(c.src))
			if !equalStrings(got, c.want) {
				t.Errorf("ParsePnpmWorkspaceYAML() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestParseCargoWorkspace(t *testing.T) {
	cases := []struct {
		name        string
		src         string
		wantMembers []string
		wantExclude []string
	}{
		{
			name:        "members and exclude",
			src:         "[workspace]\nmembers = [\"crates/*\", \"tools/gen\"]\nexclude = [\"crates/legacy\"]\n",
			wantMembers: []string{"crates/*", "tools/gen"},
			wantExclude: []string{"crates/legacy"},
		},
		{
			name:        "members only",
			src:         "[workspace]\nmembers = [\"crates/*\"]\n",
			wantMembers: []string{"crates/*"},
			wantExclude: nil,
		},
		{
			name:        "root crate that is also a workspace",
			src:         "[package]\nname = \"root\"\nversion = \"0.1.0\"\n\n[workspace]\nmembers = [\"crates/a\"]\n",
			wantMembers: []string{"crates/a"},
			wantExclude: nil,
		},
		{
			name:        "plain crate, no workspace table",
			src:         "[package]\nname = \"solo\"\n\n[dependencies]\nserde = \"1.0\"\n",
			wantMembers: nil,
			wantExclude: nil,
		},
		{
			name:        "empty input",
			src:         ``,
			wantMembers: nil,
			wantExclude: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			members, exclude := ParseCargoWorkspace([]byte(c.src))
			if !equalStrings(members, c.wantMembers) {
				t.Errorf("members = %v, want %v", members, c.wantMembers)
			}
			if !equalStrings(exclude, c.wantExclude) {
				t.Errorf("exclude = %v, want %v", exclude, c.wantExclude)
			}
		})
	}
}

func TestWorkspaceRootNodeID(t *testing.T) {
	cases := []struct {
		eco     WorkspaceEcosystem
		rootDir string
		want    string
	}{
		{WorkspaceNPM, ".", "pkgws::npm:."},
		{WorkspaceCargo, "", "pkgws::cargo:."},
		{WorkspacePnpm, "sub/dir", "pkgws::pnpm:sub/dir"},
	}
	for _, c := range cases {
		if got := WorkspaceRootNodeID(c.eco, c.rootDir); got != c.want {
			t.Errorf("WorkspaceRootNodeID(%q,%q) = %q, want %q", c.eco, c.rootDir, got, c.want)
		}
	}
}

// writeFiles materialises a map of repo-relative path → content under
// dir, creating parent directories as needed.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
}

func TestDetectAndResolveWorkspace(t *testing.T) {
	cases := []struct {
		name        string
		files       map[string]string
		wantEco     WorkspaceEcosystem
		wantMembers []string // sorted manifest paths
	}{
		{
			name: "npm workspace, glob plus explicit member",
			files: map[string]string{
				"package.json":               `{"name":"root","workspaces":["packages/*","apps/web"]}`,
				"packages/ui/package.json":    `{"name":"ui"}`,
				"packages/core/package.json":  `{"name":"core"}`,
				"packages/notes.md":           "not a package",
				"apps/web/package.json":       `{"name":"web"}`,
				"apps/admin/package.json":     `{"name":"admin"}`, // not matched — not under a pattern
			},
			wantEco: WorkspaceNPM,
			wantMembers: []string{
				"apps/web/package.json",
				"packages/core/package.json",
				"packages/ui/package.json",
			},
		},
		{
			name: "pnpm workspace, glob members",
			files: map[string]string{
				"pnpm-workspace.yaml":         "packages:\n  - 'packages/*'\n",
				"packages/a/package.json":     `{"name":"a"}`,
				"packages/b/package.json":     `{"name":"b"}`,
				"package.json":                `{"name":"root"}`,
			},
			wantEco: WorkspacePnpm,
			wantMembers: []string{
				"packages/a/package.json",
				"packages/b/package.json",
			},
		},
		{
			name: "cargo workspace, glob plus explicit, exclude honoured",
			files: map[string]string{
				"Cargo.toml":               "[workspace]\nmembers = [\"crates/*\", \"tools/gen\"]\nexclude = [\"crates/legacy\"]\n",
				"crates/engine/Cargo.toml":  "[package]\nname = \"engine\"\n",
				"crates/cli/Cargo.toml":     "[package]\nname = \"cli\"\n",
				"crates/legacy/Cargo.toml":  "[package]\nname = \"legacy\"\n", // excluded
				"tools/gen/Cargo.toml":      "[package]\nname = \"gen\"\n",
			},
			wantEco: WorkspaceCargo,
			wantMembers: []string{
				"crates/cli/Cargo.toml",
				"crates/engine/Cargo.toml",
				"tools/gen/Cargo.toml",
			},
		},
		{
			name: "non-workspace repo yields no workspace",
			files: map[string]string{
				"package.json": `{"name":"solo","version":"1.0.0","dependencies":{"react":"^18"}}`,
			},
			wantEco:     "",
			wantMembers: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			writeFiles(t, root, c.files)

			manifest := DetectWorkspace(root)
			if c.wantEco == "" {
				if manifest != nil {
					t.Fatalf("expected no workspace, got %+v", manifest)
				}
				return
			}
			if manifest == nil {
				t.Fatalf("expected a %s workspace, got nil", c.wantEco)
			}
			if manifest.Ecosystem != c.wantEco {
				t.Errorf("ecosystem = %q, want %q", manifest.Ecosystem, c.wantEco)
			}

			members := ResolveWorkspaceMembers(root, manifest)
			gotPaths := make([]string, 0, len(members))
			for _, m := range members {
				gotPaths = append(gotPaths, m.ManifestPath)
			}
			sort.Strings(gotPaths)
			if !equalStrings(gotPaths, c.wantMembers) {
				t.Errorf("members = %v, want %v", gotPaths, c.wantMembers)
			}
		})
	}
}

func TestBuildWorkspaceArtifacts(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"package.json":              `{"name":"root","workspaces":["packages/*"]}`,
		"packages/a/package.json":   `{"name":"a"}`,
		"packages/b/package.json":   `{"name":"b"}`,
	})
	manifest := DetectWorkspace(root)
	if manifest == nil {
		t.Fatal("expected an npm workspace")
	}
	members := ResolveWorkspaceMembers(root, manifest)
	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(members))
	}

	rootNode, edges := BuildWorkspaceArtifacts(manifest, members)
	if rootNode == nil {
		t.Fatal("expected a workspace root node")
	}
	if rootNode.Kind != graph.KindPackage {
		t.Errorf("root node kind = %q, want %q", rootNode.Kind, graph.KindPackage)
	}
	if rootNode.ID != "pkgws::npm:." {
		t.Errorf("root node ID = %q, want pkgws::npm:.", rootNode.ID)
	}
	if v, _ := rootNode.Meta["package_workspace"].(bool); !v {
		t.Errorf("root node should carry package_workspace=true meta")
	}
	if v, _ := rootNode.Meta["member_count"].(int); v != 2 {
		t.Errorf("member_count meta = %v, want 2", rootNode.Meta["member_count"])
	}

	if len(edges) != 2 {
		t.Fatalf("expected 2 root→member edges, got %d", len(edges))
	}
	gotTargets := make([]string, 0, len(edges))
	for _, e := range edges {
		if e.Kind != graph.EdgePackageWorkspaceMember {
			t.Errorf("edge kind = %q, want %q", e.Kind, graph.EdgePackageWorkspaceMember)
		}
		if e.From != rootNode.ID {
			t.Errorf("edge From = %q, want %q", e.From, rootNode.ID)
		}
		if e.Origin != graph.OriginASTResolved {
			t.Errorf("edge origin = %q, want %q", e.Origin, graph.OriginASTResolved)
		}
		gotTargets = append(gotTargets, e.To)
	}
	sort.Strings(gotTargets)
	want := []string{"packages/a/package.json", "packages/b/package.json"}
	if !equalStrings(gotTargets, want) {
		t.Errorf("edge targets = %v, want %v", gotTargets, want)
	}
}

func TestBuildWorkspaceArtifacts_NoMembers(t *testing.T) {
	m := &WorkspaceManifest{Ecosystem: WorkspaceNPM, ManifestPath: "package.json"}
	node, edges := BuildWorkspaceArtifacts(m, nil)
	if node != nil || edges != nil {
		t.Errorf("a workspace with no members should yield no artifacts, got node=%v edges=%v", node, edges)
	}
}

// equalStrings compares two string slices for order-sensitive equality,
// treating nil and empty as equal.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
