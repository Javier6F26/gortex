// Package protobuf re-exports the tree-sitter-proto grammar. The C
// parser lives in the sibling github.com/gortexhq/tree-sitter-protobuf
// module; this file is just the thin shim that bridges the upstream
// binding into gortex's *tsitter.Language type.
package protobuf

import (
	tree_sitter_protobuf "github.com/gortexhq/tree-sitter-protobuf/bindings/go"
	"github.com/zzet/gortex/internal/parser/tsitter"
)

// GetLanguage returns the compiled Protobuf language.
func GetLanguage() *tsitter.Language {
	return tsitter.NewLanguage(tree_sitter_protobuf.Language())
}
