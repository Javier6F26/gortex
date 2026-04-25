// Package swift re-exports the tree-sitter-swift grammar. The C parser
// lives in the sibling github.com/gortexhq/tree-sitter-swift module (a
// fork of the tree-sitter-recommended alex-pinkus/tree-sitter-swift
// with a regenerated parser.c committed); this file is just the thin
// shim that bridges the upstream binding into gortex's *tsitter.Language
// type.
package swift

import (
	tree_sitter_swift "github.com/gortexhq/tree-sitter-swift/bindings/go"
	"github.com/zzet/gortex/internal/parser/tsitter"
)

// GetLanguage returns the compiled Swift language.
func GetLanguage() *tsitter.Language {
	return tsitter.NewLanguage(tree_sitter_swift.Language())
}
