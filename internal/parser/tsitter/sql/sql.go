// Package sql re-exports the tree-sitter-sql grammar. The C parser
// lives in the sibling github.com/gortexhq/tree-sitter-sql module;
// this file is just the thin shim that bridges the upstream binding
// into gortex's *tsitter.Language type.
package sql

import (
	tree_sitter_sql "github.com/gortexhq/tree-sitter-sql/bindings/go"
	"github.com/zzet/gortex/internal/parser/tsitter"
)

// GetLanguage returns the compiled SQL language.
func GetLanguage() *tsitter.Language {
	return tsitter.NewLanguage(tree_sitter_sql.Language())
}
