// Package markdown re-exports the tree-sitter-markdown grammar. The C
// parser lives in the sibling github.com/gortexhq/tree-sitter-markdown
// module (a fork of the tree-sitter-recommended
// tree-sitter-grammars/tree-sitter-markdown block-level parser with
// Go bindings added); this file is just the thin shim that bridges
// the upstream binding into gortex's *tsitter.Language type.
package markdown

import (
	tree_sitter_markdown "github.com/gortexhq/tree-sitter-markdown/bindings/go"
	"github.com/zzet/gortex/internal/parser/tsitter"
)

// GetLanguage returns the compiled Markdown language.
func GetLanguage() *tsitter.Language {
	return tsitter.NewLanguage(tree_sitter_markdown.Language())
}
