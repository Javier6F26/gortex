package modules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/zzet/gortex/internal/graph"
)

// A package-manager workspace is a root manifest that declares a set of
// member packages — distinct from Gortex's repository-grouping
// WorkspaceID and from cross-repo edges. Three ecosystems express it:
//
//   - npm / yarn: a root package.json with a "workspaces" array (or the
//     `{ "workspaces": { "packages": [...] } }` object form yarn also
//     accepts). Entries are glob patterns or plain directory paths.
//   - pnpm: a pnpm-workspace.yaml with a top-level `packages:` list.
//   - Cargo: a root Cargo.toml with `[workspace] members` (and an
//     optional `exclude` list). Entries may be globs.
//
// Detection produces a WorkspaceManifest; ResolveWorkspaceMembers then
// expands the glob patterns against the filesystem to the concrete
// member manifest files. BuildWorkspaceArtifacts turns the resolved
// members into a root node plus one EdgePackageWorkspaceMember edge per
// member.

// WorkspaceEcosystem identifies which package manager owns a workspace
// root. The string value also namespaces the synthetic root node ID.
type WorkspaceEcosystem string

const (
	// WorkspaceNPM covers both npm and yarn — they share the
	// package.json "workspaces" field and the same glob semantics, so
	// a single ecosystem tag is enough.
	WorkspaceNPM   WorkspaceEcosystem = "npm"
	WorkspacePnpm  WorkspaceEcosystem = "pnpm"
	WorkspaceCargo WorkspaceEcosystem = "cargo"
)

// WorkspaceManifest is a detected package-manager workspace root: which
// ecosystem owns it, the repo-relative path of the root manifest file,
// and the verbatim member patterns it declares. Patterns are not yet
// resolved — ResolveWorkspaceMembers expands them.
type WorkspaceManifest struct {
	Ecosystem WorkspaceEcosystem
	// ManifestPath is the repo-relative path of the root manifest
	// (e.g. "package.json", "pnpm-workspace.yaml", "Cargo.toml").
	ManifestPath string
	// Patterns are the declared member entries — glob patterns
	// (`packages/*`) or plain directory paths (`apps/web`).
	Patterns []string
	// Exclude holds patterns explicitly excluded from membership.
	// Only Cargo's `[workspace] exclude` populates this; npm/pnpm have
	// no exclude list, so it stays nil for them.
	Exclude []string
}

// WorkspaceMember is one resolved member package of a workspace: the
// repo-relative path of its own manifest file (the file node the
// membership edge targets) and the repo-relative directory it lives in.
type WorkspaceMember struct {
	// ManifestPath is the repo-relative path of the member's manifest
	// (package.json for npm/pnpm, Cargo.toml for cargo).
	ManifestPath string
	// Dir is the repo-relative directory of the member package.
	Dir string
}

// memberManifestName returns the manifest filename a member package of
// the given ecosystem carries at its directory root.
func memberManifestName(eco WorkspaceEcosystem) string {
	if eco == WorkspaceCargo {
		return "Cargo.toml"
	}
	return "package.json"
}

// ParsePackageJSONWorkspaces inspects an npm/yarn root package.json and
// returns the declared workspace member patterns. Both the array form
// (`"workspaces": ["packages/*"]`) and the object form yarn also
// accepts (`"workspaces": { "packages": ["packages/*"] }`) are handled.
// Returns nil when the manifest declares no workspaces — that repo is
// simply not a workspace root.
func ParsePackageJSONWorkspaces(source []byte) []string {
	if len(source) == 0 {
		return nil
	}
	// The "workspaces" field is polymorphic: a JSON array of strings,
	// or an object with a "packages" array (and an optional "nohoist"
	// the graph doesn't care about). Decode into json.RawMessage first
	// so we can branch on the concrete shape.
	var manifest struct {
		Workspaces json.RawMessage `json:"workspaces"`
	}
	if err := json.Unmarshal(source, &manifest); err != nil {
		return nil
	}
	if len(manifest.Workspaces) == 0 {
		return nil
	}
	// Array form.
	var asArray []string
	if err := json.Unmarshal(manifest.Workspaces, &asArray); err == nil {
		return cleanPatterns(asArray)
	}
	// Object form.
	var asObject struct {
		Packages []string `json:"packages"`
	}
	if err := json.Unmarshal(manifest.Workspaces, &asObject); err == nil {
		return cleanPatterns(asObject.Packages)
	}
	return nil
}

// ParsePnpmWorkspaceYAML inspects a pnpm-workspace.yaml file and returns
// the declared member patterns from its top-level `packages:` list. The
// file is parsed with a line-oriented scan rather than a YAML library:
// pnpm-workspace.yaml is tiny and flat, and the scan keeps the parser
// dependency-free and independent of YAML-library list-style quirks
// (block sequences vs. flow sequences). Returns nil when no `packages:`
// list is present.
func ParsePnpmWorkspaceYAML(source []byte) []string {
	if len(source) == 0 {
		return nil
	}
	var (
		patterns  []string
		inPackages bool
	)
	for _, raw := range strings.Split(string(source), "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// A non-indented line starts a new top-level key. `packages:`
		// opens the section we want; any other top-level key closes it.
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			// The key is the text before the first ':'. Taking the
			// substring (rather than trimming a trailing ':') keeps
			// `packages: [...]` — an inline flow sequence on the same
			// line — recognised as the `packages` key.
			key := trimmed
			if i := strings.IndexByte(trimmed, ':'); i >= 0 {
				key = trimmed[:i]
			}
			key = strings.TrimSpace(key)
			// `packages:` may inline a flow sequence on the same line
			// (`packages: ["a/*", "b/*"]`) — handle that before
			// switching into block-sequence mode.
			if key == "packages" {
				if idx := strings.Index(trimmed, "["); idx >= 0 {
					patterns = append(patterns, parseYAMLFlowSequence(trimmed[idx:])...)
					inPackages = false
					continue
				}
				inPackages = true
				continue
			}
			inPackages = false
			continue
		}
		if !inPackages {
			continue
		}
		// Inside `packages:` — block-sequence entries look like
		// `  - 'packages/*'`.
		if !strings.HasPrefix(trimmed, "-") {
			continue
		}
		item := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
		item = unquoteYAMLScalar(item)
		if item != "" {
			patterns = append(patterns, item)
		}
	}
	return cleanPatterns(patterns)
}

// ParseCargoWorkspace inspects a Cargo.toml and returns the member and
// exclude patterns from its `[workspace]` table. A Cargo manifest can
// be a workspace root, a regular crate, or both at once (a root crate);
// only the `[workspace]` table matters here. Returns (nil, nil) when
// the manifest has no `[workspace]` table — that crate is not a
// workspace root.
func ParseCargoWorkspace(source []byte) (members, exclude []string) {
	if len(source) == 0 {
		return nil, nil
	}
	var manifest struct {
		Workspace *struct {
			Members []string `toml:"members"`
			Exclude []string `toml:"exclude"`
		} `toml:"workspace"`
	}
	if err := toml.Unmarshal(source, &manifest); err != nil {
		return nil, nil
	}
	if manifest.Workspace == nil {
		return nil, nil
	}
	return cleanPatterns(manifest.Workspace.Members), cleanPatterns(manifest.Workspace.Exclude)
}

// DetectWorkspace reads the candidate root manifests at repoRoot and
// returns the first package-manager workspace it finds, or nil when the
// repo is not a workspace root. Detection order is npm/yarn → pnpm →
// Cargo; a repo realistically uses one package manager, so the first
// hit is the answer. repoRoot is an absolute filesystem path.
func DetectWorkspace(repoRoot string) *WorkspaceManifest {
	if repoRoot == "" {
		return nil
	}
	// npm / yarn — root package.json with a "workspaces" field.
	if src, err := os.ReadFile(filepath.Join(repoRoot, "package.json")); err == nil {
		if pats := ParsePackageJSONWorkspaces(src); len(pats) > 0 {
			return &WorkspaceManifest{
				Ecosystem:    WorkspaceNPM,
				ManifestPath: "package.json",
				Patterns:     pats,
			}
		}
	}
	// pnpm — pnpm-workspace.yaml with a `packages:` list.
	if src, err := os.ReadFile(filepath.Join(repoRoot, "pnpm-workspace.yaml")); err == nil {
		if pats := ParsePnpmWorkspaceYAML(src); len(pats) > 0 {
			return &WorkspaceManifest{
				Ecosystem:    WorkspacePnpm,
				ManifestPath: "pnpm-workspace.yaml",
				Patterns:     pats,
			}
		}
	}
	// Cargo — root Cargo.toml with a `[workspace]` table.
	if src, err := os.ReadFile(filepath.Join(repoRoot, "Cargo.toml")); err == nil {
		if members, exclude := ParseCargoWorkspace(src); len(members) > 0 {
			return &WorkspaceManifest{
				Ecosystem:    WorkspaceCargo,
				ManifestPath: "Cargo.toml",
				Patterns:     members,
				Exclude:      exclude,
			}
		}
	}
	return nil
}

// ResolveWorkspaceMembers expands a WorkspaceManifest's member patterns
// against the filesystem under repoRoot (an absolute path) and returns
// one WorkspaceMember per directory that both matches a member pattern
// and actually carries the ecosystem's manifest file. Glob patterns
// (`packages/*`, `crates/*`) and plain directory paths are both
// supported; a directory matched by an exclude pattern is dropped.
//
// The workspace root's own directory is never returned as its own
// member — a root crate that also lists itself resolves to the root,
// which BuildWorkspaceArtifacts skips. Results are sorted by manifest
// path for deterministic edge emission.
func ResolveWorkspaceMembers(repoRoot string, m *WorkspaceManifest) []WorkspaceMember {
	if repoRoot == "" || m == nil {
		return nil
	}
	manifestName := memberManifestName(m.Ecosystem)
	excluded := make(map[string]struct{})
	for _, pat := range m.Exclude {
		for _, dir := range expandMemberPattern(repoRoot, pat) {
			excluded[dir] = struct{}{}
		}
	}
	seen := make(map[string]struct{})
	var members []WorkspaceMember
	for _, pat := range m.Patterns {
		for _, dir := range expandMemberPattern(repoRoot, pat) {
			if _, skip := excluded[dir]; skip {
				continue
			}
			// The root manifest's own directory (".") is the workspace
			// root, not a member of itself.
			if dir == "." {
				continue
			}
			if _, dup := seen[dir]; dup {
				continue
			}
			manifestRel := filepath.ToSlash(filepath.Join(dir, manifestName))
			// A member directory only counts when it carries the
			// ecosystem's manifest — a `packages/*` glob can match
			// non-package directories (docs, fixtures) that should
			// not become graph members.
			if !fileExists(filepath.Join(repoRoot, filepath.FromSlash(manifestRel))) {
				continue
			}
			seen[dir] = struct{}{}
			members = append(members, WorkspaceMember{
				ManifestPath: manifestRel,
				Dir:          dir,
			})
		}
	}
	sort.Slice(members, func(i, j int) bool {
		return members[i].ManifestPath < members[j].ManifestPath
	})
	return members
}

// expandMemberPattern resolves one member pattern to the set of
// repo-relative directories it matches. A pattern with no glob
// metacharacter is treated as a literal directory path; a pattern with
// `*` / `?` / `[` is expanded with filepath.Glob. Only directories are
// returned. The returned paths are slash-separated and repo-relative;
// the root itself is returned as ".".
func expandMemberPattern(repoRoot, pattern string) []string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil
	}
	// Normalise to forward slashes and strip a trailing slash so
	// `packages/` and `packages` behave identically.
	pattern = strings.TrimSuffix(filepath.ToSlash(pattern), "/")
	if pattern == "" || pattern == "." {
		return []string{"."}
	}
	if !strings.ContainsAny(pattern, "*?[") {
		// Literal directory path.
		abs := filepath.Join(repoRoot, filepath.FromSlash(pattern))
		if dirExists(abs) {
			return []string{pattern}
		}
		return nil
	}
	// Glob pattern — expand against the filesystem.
	matches, err := filepath.Glob(filepath.Join(repoRoot, filepath.FromSlash(pattern)))
	if err != nil {
		return nil
	}
	var dirs []string
	for _, abs := range matches {
		if !dirExists(abs) {
			continue
		}
		rel, err := filepath.Rel(repoRoot, abs)
		if err != nil {
			continue
		}
		dirs = append(dirs, filepath.ToSlash(rel))
	}
	sort.Strings(dirs)
	return dirs
}

// WorkspaceRootNodeID returns the canonical ID for a workspace-root
// node. The `pkgws::` prefix is a synthetic namespace (matching the
// `module::` / `external::` convention the exporter already
// recognises); ecosystem plus the repo-relative root directory keeps
// two workspace roots in the same repo — vanishingly rare, but a
// nested Cargo workspace inside an npm one is legal — distinct.
func WorkspaceRootNodeID(eco WorkspaceEcosystem, rootDir string) string {
	rootDir = filepath.ToSlash(strings.TrimSpace(rootDir))
	if rootDir == "" {
		rootDir = "."
	}
	return "pkgws::" + string(eco) + ":" + rootDir
}

// BuildWorkspaceArtifacts turns a detected workspace and its resolved
// members into a (root node, edges) pair: one synthetic
// KindPackage root node and one EdgePackageWorkspaceMember edge from
// that root to each member's manifest file node. Returns (nil, nil)
// when there are no members — a workspace root with zero resolved
// members carries no graph signal.
//
// The root node's FilePath is the root manifest so navigation lands on
// the file declaring the workspace; Meta records the ecosystem and
// member count. Edge endpoints are unprefixed repo-relative paths —
// applyRepoPrefix downstream handles multi-repo namespacing.
func BuildWorkspaceArtifacts(m *WorkspaceManifest, members []WorkspaceMember) (*graph.Node, []*graph.Edge) {
	if m == nil || len(members) == 0 {
		return nil, nil
	}
	rootDir := filepath.ToSlash(filepath.Dir(m.ManifestPath))
	rootID := WorkspaceRootNodeID(m.Ecosystem, rootDir)
	root := &graph.Node{
		ID:       rootID,
		Kind:     graph.KindPackage,
		Name:     "workspace:" + string(m.Ecosystem),
		FilePath: filepath.ToSlash(m.ManifestPath),
		Language: manifestLanguage(m.ManifestPath),
		Meta: map[string]any{
			"package_workspace": true,
			"ecosystem":         string(m.Ecosystem),
			"manifest":          filepath.ToSlash(m.ManifestPath),
			"member_count":      len(members),
		},
	}
	edges := make([]*graph.Edge, 0, len(members))
	for _, mem := range members {
		edges = append(edges, &graph.Edge{
			From:     rootID,
			To:       mem.ManifestPath,
			Kind:     graph.EdgePackageWorkspaceMember,
			FilePath: filepath.ToSlash(m.ManifestPath),
			Origin:   graph.OriginASTResolved,
			Meta: map[string]any{
				"ecosystem":  string(m.Ecosystem),
				"member_dir": mem.Dir,
			},
		})
	}
	return root, edges
}

// cleanPatterns trims whitespace from each pattern and drops empties,
// preserving order.
func cleanPatterns(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseYAMLFlowSequence parses a single-line YAML flow sequence
// (`["a/*", 'b/*']`) into its scalar entries. Only the flat
// string-list shape pnpm-workspace.yaml uses is supported.
func parseYAMLFlowSequence(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		item := unquoteYAMLScalar(strings.TrimSpace(part))
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

// unquoteYAMLScalar strips a single matching pair of single or double
// quotes from a YAML scalar. Unquoted scalars are returned unchanged.
func unquoteYAMLScalar(s string) string {
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// dirExists reports whether absPath exists and is a directory.
func dirExists(absPath string) bool {
	info, err := os.Stat(absPath)
	return err == nil && info.IsDir()
}

// fileExists reports whether absPath exists and is a regular file.
func fileExists(absPath string) bool {
	info, err := os.Stat(absPath)
	return err == nil && !info.IsDir()
}

// manifestLanguage maps a workspace manifest path to the language tag
// stamped on its synthetic node. Kept local to the modules package so
// it does not depend on the indexer's identically-named helper.
func manifestLanguage(manifestPath string) string {
	switch filepath.Base(manifestPath) {
	case "package.json":
		return "json"
	case "pnpm-workspace.yaml":
		return "yaml"
	case "Cargo.toml":
		return "toml"
	}
	return ""
}
