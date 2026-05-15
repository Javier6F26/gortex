package resolver

// LSPHelper drives resolve-time LSP queries from the cross-file
// resolver. The resolver consults it for TS/JS/JSX/TSX edges before
// falling back to AST/name heuristics — letting the type-aware
// compiler (tsserver) win on cases the heuristics lose: barrel
// re-exports, declaration merging, type-narrowed dispatch, JSX
// component-as-callsite.
//
// The interface is defined in the resolver package so the resolver
// has no compile-time dependency on the lsp package — the indexer
// constructs a concrete helper (typically wrapping a *lsp.Provider)
// and injects it via Resolver.SetLSPHelper. Resolver consults the
// helper synchronously during resolveEdge; implementations are
// expected to serialise tsserver-bound calls themselves and apply a
// per-call timeout so a stalled language server can never gate the
// resolve pass.
type LSPHelper interface {
	// SupportsPath reports whether the helper can answer queries for
	// relPath. Implementations match on file extension; the resolver
	// short-circuits when SupportsPath is false (no LSP attempt).
	SupportsPath(relPath string) bool

	// Definition returns the (relativePath, 1-based line) of the
	// declaration of `name` referenced on `oneBasedLine` inside
	// relPath. Returns ok=false when the LSP is unavailable, times
	// out, or has no answer. The caller is responsible for matching
	// the returned location to a graph node.
	Definition(relPath string, oneBasedLine int, name string) (defRelPath string, defOneBasedLine int, ok bool)
}

// SetLSPHelper installs a resolve-time LSP helper. Pass nil to detach.
// Must be called before ResolveAll / ResolveFile — the resolver caches
// no LSP state across passes, so changing helpers between passes is
// safe but mid-pass installation is racy with the parallel resolveEdge
// workers and is not supported.
func (r *Resolver) SetLSPHelper(h LSPHelper) {
	r.lspHelper = h
}

// LSPHelper returns the currently installed helper, or nil.
func (r *Resolver) LSPHelper() LSPHelper {
	return r.lspHelper
}
