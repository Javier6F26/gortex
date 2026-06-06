package agents

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// render.go is the engine behind the skill-render drift fence. It runs
// every adapter against an isolated sandbox (a throwaway HOME + repo
// root) with ForceDetect on — so the adapter renders regardless of which
// tools are installed — then serialises the produced file tree into a
// stable, machine-independent text manifest. The manifest is
// byte-compared against committed goldens by the drift test and the
// `gortex agents render` command, so any change to an adapter's
// generated MCP config, instructions, hooks, or skills surfaces as a
// reviewable diff across every platform, not just Claude.

// RenderManifest renders each adapter into its own sandbox and returns a
// normalised manifest per adapter, keyed by adapter name.
func RenderManifest(adapters []Adapter) (map[string]string, error) {
	out := make(map[string]string, len(adapters))
	for _, a := range adapters {
		m, err := renderOne(a)
		if err != nil {
			return nil, fmt.Errorf("render %s: %w", a.Name(), err)
		}
		out[a.Name()] = m
	}
	return out, nil
}

// renderOne applies a single adapter in a fresh sandbox and returns its
// manifest. The HOME and repo-root temp dirs are removed afterwards.
func renderOne(a Adapter) (string, error) {
	home, err := os.MkdirTemp("", "gortex-render-home-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(home) }()
	root, err := os.MkdirTemp("", "gortex-render-root-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(root) }()

	// Project mode is the default `gortex init` path and the one that
	// renders the per-repo skill / instruction surfaces (where content
	// drift lives). A fixed SkillsRouting payload makes the
	// community-routing blocks render deterministically.
	env := Env{
		Root:          root,
		Home:          home,
		Mode:          ModeProject,
		HookCommand:   "gortex hook",
		SkillsRouting: "- [example-community](.claude/skills/example/SKILL.md) — example routing block\n",
		Stderr:        io.Discard,
	}
	if _, err := a.Apply(env, ApplyOpts{ForceDetect: true}); err != nil {
		return "", err
	}
	return manifestForDirs(home, root)
}

// manifestForDirs walks the sandbox HOME and repo root and builds a
// sorted, normalised manifest. Each file becomes a `=== <key> ===`
// header followed by its (sandbox-path-stripped) content; entries are
// sorted by key so the manifest is deterministic.
func manifestForDirs(home, root string) (string, error) {
	type entry struct{ key, content string }
	var entries []entry

	collect := func(base, prefix string) error {
		return filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(base, path)
			if err != nil {
				return err
			}
			entries = append(entries, entry{
				key:     canonicalManifestKey(prefix + filepath.ToSlash(rel)),
				content: normalizeRender(string(data), home, root),
			})
			return nil
		})
	}
	if err := collect(home, "home/"); err != nil {
		return "", err
	}
	if err := collect(root, "root/"); err != nil {
		return "", err
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "=== %s ===\n%s\n", e.key, strings.TrimRight(e.content, "\n"))
	}
	return b.String(), nil
}

// gortexBinaryPaths memoizes the absolute paths an adapter might bake
// into its config for the gortex binary — the running process
// (os.Executable, as the hermes adapter uses) and a PATH lookup. They
// are normalised to bare "gortex" so the manifest doesn't depend on
// where this build happens to live (dev box vs CI vs `go test` binary).
var gortexBinaryPaths = sync.OnceValue(func() []string {
	seen := map[string]struct{}{}
	var paths []string
	add := func(p string) {
		if p == "" || p == "gortex" {
			return
		}
		if _, dup := seen[p]; dup {
			return
		}
		seen[p] = struct{}{}
		paths = append(paths, p)
	}
	if exe, err := os.Executable(); err == nil {
		add(exe)
		if abs, e := filepath.Abs(exe); e == nil {
			add(abs)
		}
	}
	if p, err := exec.LookPath("gortex"); err == nil {
		add(p)
	}
	return paths
})

// canonicalManifestKey rewrites the OS-specific user-config locations
// in a manifest path to a single canonical (Linux/XDG) form, so the
// manifest is identical regardless of the OS the render runs on.
// Without this an adapter that uses OS-specific config dirs makes the
// golden non-portable (e.g. zed writes ~/Library/Application Support/Zed
// on macOS, ~/AppData/Roaming/Zed on Windows, and ~/.config/zed on
// Linux). Only manifest keys are folded; file contents are compared
// verbatim.
func canonicalManifestKey(key string) string {
	key = strings.ReplaceAll(key, "Library/Application Support/", ".config/")
	key = strings.ReplaceAll(key, "AppData/Roaming/", ".config/")
	// zed capitalises its directory on macOS/Windows but not on Linux.
	key = strings.ReplaceAll(key, ".config/Zed/", ".config/zed/")
	return key
}

// normalizeRender replaces machine-specific absolutes — the sandbox
// HOME / repo root and the resolved gortex binary path — with stable
// placeholders so the manifest is identical on every machine.
func normalizeRender(s, home, root string) string {
	s = strings.ReplaceAll(s, home, "$HOME")
	s = strings.ReplaceAll(s, root, "$ROOT")
	for _, p := range gortexBinaryPaths() {
		s = strings.ReplaceAll(s, p, "gortex")
	}
	return s
}

// RenderContainsRegistration reports whether a rendered manifest still
// wires Gortex in — an MCP server stanza, a gortex hook, a community
// routing block, or instruction prose all reference "gortex". The drift
// CLI uses it as a structural sanity check (independent of the byte
// golden) that an adapter didn't silently stop emitting gortex content.
func RenderContainsRegistration(manifest string) bool {
	return strings.Contains(strings.ToLower(manifest), "gortex")
}
