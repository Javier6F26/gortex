package mcp

import (
	"sync"

	"github.com/zzet/gortex/internal/parser"
	"github.com/zzet/gortex/internal/parser/languages"
)

// testRegistry returns a package-wide parser.Registry with every language
// extractor registered. RegisterAll recompiles ~30 tree-sitter grammar
// queries (measured ~345ms/call) — sharing one instance across this
// package's many test-server helpers avoids paying that cost at each of
// their call sites. Safe to share: no test in this package registers
// additional extractors on top of RegisterAll, mutates the registry, or
// runs in parallel with another.
var (
	testRegistryOnce      sync.Once
	testRegistrySingleton *parser.Registry
)

func testRegistry() *parser.Registry {
	testRegistryOnce.Do(func() {
		testRegistrySingleton = parser.NewRegistry()
		languages.RegisterAll(testRegistrySingleton)
	})
	return testRegistrySingleton
}
