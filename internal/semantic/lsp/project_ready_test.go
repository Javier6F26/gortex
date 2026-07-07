package lsp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTSProjectReady(t *testing.T) {
	mk := func(t *testing.T, files, dirs []string) string {
		t.Helper()
		root := t.TempDir()
		for _, d := range dirs {
			if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for _, f := range files {
			p := filepath.Join(root, f)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}

	t.Run("tsconfig without node_modules is not ready", func(t *testing.T) {
		root := mk(t, []string{"frontend/tsconfig.json"}, nil)
		ready, rem := tsProjectReady(root)
		if ready {
			t.Fatalf("want not ready, got ready")
		}
		if rem == "" {
			t.Fatalf("want a remediation string")
		}
	})

	t.Run("tsconfig with node_modules is ready", func(t *testing.T) {
		root := mk(t, []string{"frontend/tsconfig.json"}, []string{"frontend/node_modules/react"})
		if ready, _ := tsProjectReady(root); !ready {
			t.Fatalf("want ready when node_modules present")
		}
	})

	t.Run("no tsconfig is left alone (ready)", func(t *testing.T) {
		root := mk(t, []string{"src/app.js"}, nil)
		if ready, _ := tsProjectReady(root); !ready {
			t.Fatalf("want ready when there is no tsconfig (loose JS)")
		}
	})

	t.Run("node_modules deeper than tsconfig still counts", func(t *testing.T) {
		root := mk(t, []string{"tsconfig.json"}, []string{"packages/web/node_modules/x"})
		if ready, _ := tsProjectReady(root); !ready {
			t.Fatalf("want ready when node_modules exists anywhere")
		}
	})

	t.Run("empty root is ready", func(t *testing.T) {
		if ready, _ := tsProjectReady(""); !ready {
			t.Fatalf("want ready for empty root")
		}
	})
}
