// Package dart re-exports the tree-sitter-dart grammar. The C parser
// lives in the sibling github.com/gortexhq/tree-sitter-dart module;
// this file is just the thin shim that bridges the upstream binding
// into gortex's *tsitter.Language type.
package dart

import (
	tree_sitter_dart "github.com/gortexhq/tree-sitter-dart/bindings/go"
	"github.com/zzet/gortex/internal/parser/tsitter"
)

// GetLanguage returns the compiled Dart language.
func GetLanguage() *tsitter.Language {
	return tsitter.NewLanguage(tree_sitter_dart.Language())
}
