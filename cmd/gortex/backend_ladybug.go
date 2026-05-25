//go:build ladybug

package main

import (
	"fmt"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/graph/store_ladybug"
)

// openLadybugBackend opens (or creates) the ladybug store at
// path. Returns a cleanup func that closes the underlying handle
// — important because ladybug's writer locks the directory and
// a subsequent reopen on the same path would fail until the
// previous handle is closed.
func openLadybugBackend(path string) (graph.Store, func(), error) {
	s, err := store_ladybug.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open ladybug store at %q: %w", path, err)
	}
	return s, func() { _ = s.Close() }, nil
}
