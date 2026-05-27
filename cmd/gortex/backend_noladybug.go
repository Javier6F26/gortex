//go:build !ladybug

package main

import (
	"fmt"

	"github.com/zzet/gortex/internal/graph"
)

// openLadybugBackend is the no-op fallback used when the binary
// was built without `-tags ladybug`. Returning an error here
// (instead of panicking) lets the caller surface a clear
// "rebuild with -tags ladybug" message instead of crashing the
// daemon on startup.
func openLadybugBackend(path string, bufferPoolMB uint64) (graph.Store, func(), error) {
	return nil, nil, fmt.Errorf("ladybug backend requested but binary was built without -tags ladybug; rebuild with: go build -tags ladybug ./cmd/gortex")
}
