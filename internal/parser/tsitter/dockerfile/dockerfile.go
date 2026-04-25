// Package dockerfile re-exports the tree-sitter-dockerfile grammar.
// The C parser lives in the sibling github.com/gortexhq/tree-sitter-dockerfile
// module; this file is just the thin shim that bridges the upstream
// binding into gortex's *tsitter.Language type.
package dockerfile

import (
	tree_sitter_dockerfile "github.com/gortexhq/tree-sitter-dockerfile/bindings/go"
	"github.com/zzet/gortex/internal/parser/tsitter"
)

// GetLanguage returns the compiled Dockerfile language.
func GetLanguage() *tsitter.Language {
	return tsitter.NewLanguage(tree_sitter_dockerfile.Language())
}
