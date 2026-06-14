package mcp

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func runGitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	require.NoError(t, cmd.Run(), "git %v", args)
}

func TestAutoIndexHelpers(t *testing.T) {
	dir := t.TempDir()

	// Not a git repo: no root, and ls-files errors so the size bound never
	// blocks (returns not-over).
	require.Equal(t, "", gitRepoRoot(dir))
	if _, over := repoFileCountOverLimit(dir, 0); over {
		t.Error("a non-git dir must not be flagged over the limit")
	}

	// Make it a git repo with two tracked files.
	runGitT(t, dir, "init")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b\n"), 0o644))
	runGitT(t, dir, "add", ".")

	root := gitRepoRoot(dir)
	require.NotEmpty(t, root)
	rootResolved, _ := filepath.EvalSymlinks(root)
	dirResolved, _ := filepath.EvalSymlinks(dir)
	require.Equal(t, dirResolved, rootResolved, "git root must resolve to the repo dir")

	n, over := repoFileCountOverLimit(dir, 100)
	require.Equal(t, 2, n, "two tracked files")
	require.False(t, over)

	if _, over := repoFileCountOverLimit(dir, 1); !over {
		t.Error("two files must exceed a limit of one")
	}
}
