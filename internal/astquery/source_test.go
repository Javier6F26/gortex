package astquery

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The Source seam lets the engine parse a target's bytes from an in-memory
// provider (a follower's file_blobs) instead of disk, with results identical
// to a disk-backed run over the same content (code-source-blobs D7: search_ast
// works diskless).
func TestSource_BlobVsDiskParity(t *testing.T) {
	src := `package x

func F() {
	panic("boom")
}

func G() {
	panic("again")
}
`
	pattern := `((call_expression function: (identifier) @fn) @match (#eq? @fn "panic"))`

	// Disk-backed run.
	root := t.TempDir()
	abs := filepath.Join(root, "lib.go")
	require.NoError(t, os.WriteFile(abs, []byte(src), 0o644))

	targets := []Target{{AbsPath: abs, GraphPath: "repo/lib.go", Language: "go"}}
	diskRes, err := Run(context.Background(), Options{
		Pattern:  pattern,
		Language: "go",
		Targets:  targets,
		Resolver: DefaultLanguageResolver,
	})
	require.NoError(t, err)
	require.Equal(t, 2, diskRes.Total, "two panic() calls on disk")

	// Blob-backed run: no AbsPath on disk, bytes come from Source keyed on
	// GraphPath.
	blobTargets := []Target{{AbsPath: "", GraphPath: "repo/lib.go", Language: "go"}}
	blobRes, err := Run(context.Background(), Options{
		Pattern:  pattern,
		Language: "go",
		Targets:  blobTargets,
		Resolver: DefaultLanguageResolver,
		Source: func(tg Target) ([]byte, error) {
			if tg.GraphPath == "repo/lib.go" {
				return []byte(src), nil
			}
			return nil, os.ErrNotExist
		},
	})
	require.NoError(t, err)
	require.Equal(t, diskRes.Total, blobRes.Total, "blob parity: same match count")
	require.Len(t, blobRes.Matches, len(diskRes.Matches))
	for i := range diskRes.Matches {
		require.Equal(t, diskRes.Matches[i].Line, blobRes.Matches[i].Line, "match %d line parity", i)
		require.Equal(t, diskRes.Matches[i].File, blobRes.Matches[i].File, "match %d file parity", i)
	}
}

// A Source that errors on a target skips it (recorded as a per-file error),
// exactly like an unreadable disk file — the run does not fail.
func TestSource_ErrorSkipsTarget(t *testing.T) {
	res, err := Run(context.Background(), Options{
		Pattern:  `((call_expression function: (identifier) @fn) @match (#eq? @fn "panic"))`,
		Language: "go",
		Targets:  []Target{{GraphPath: "repo/missing.go", Language: "go"}},
		Resolver: DefaultLanguageResolver,
		Source:   func(Target) ([]byte, error) { return nil, os.ErrNotExist },
	})
	require.NoError(t, err)
	require.Equal(t, 0, res.Total)
}
