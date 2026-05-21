package resolver

import "strings"

// NpmAliasResolver rewrites a JS/TS import specifier when it resolves
// through an npm alias. npm lets a package.json dependency be declared
// as `"shared": "npm:@acme/shared-lib@1.4.0"`; an `import x from
// 'shared'` then actually refers to the package `@acme/shared-lib`.
// Without the rewrite the resolver treats the bare specifier as an
// external dependency and the cross-package edge to a locally-vendored
// `@acme/shared-lib` is lost.
//
// Given the importing file's repo-prefixed graph path and the verbatim
// import specifier, the implementation finds the nearest-ancestor
// package.json, checks its dependencies / devDependencies for an
// npm-alias entry keyed by the specifier's package portion, and
// returns the specifier with that portion swapped for the alias's real
// package name (a sub-path like `shared/util` keeps its `/util` tail).
// It returns "" when the specifier is not an npm alias — the caller
// then resolves the original specifier unchanged.
//
// The type is defined in the resolver package so the resolver has no
// compile-time dependency on the indexer or the filesystem — the
// indexer constructs a concrete implementation (which reads
// package.json from disk) and injects it via SetNpmAliasResolver.
type NpmAliasResolver func(callerFile, specifier string) string

// SetNpmAliasResolver installs an npm-alias import rewriter. Pass nil
// to detach. Must be called before ResolveAll / ResolveFile — the
// resolver caches no alias state across passes, so mid-pass swaps are
// racy with the parallel resolveEdge workers and are not supported.
func (r *Resolver) SetNpmAliasResolver(fn NpmAliasResolver) {
	r.npmAlias = fn
}

// SetNpmAliasResolver installs an npm-alias import rewriter on the
// cross-repo resolver. Same contract as the Resolver method.
func (cr *CrossRepoResolver) SetNpmAliasResolver(fn NpmAliasResolver) {
	cr.npmAlias = fn
}

// rewriteNpmAliasImport applies the installed NpmAliasResolver to an
// import specifier. It returns the (possibly rewritten) specifier and
// whether a rewrite happened. When no resolver is installed or the
// specifier is not an npm alias the specifier is returned unchanged
// with rewritten=false. Shared by Resolver.resolveImport and
// CrossRepoResolver.resolveImport so both resolution passes treat
// npm-aliased imports identically.
func rewriteNpmAliasImport(fn NpmAliasResolver, callerFile, importPath string) (string, bool) {
	if fn == nil || callerFile == "" || importPath == "" {
		return importPath, false
	}
	if real := fn(callerFile, importPath); real != "" && real != importPath {
		return real, true
	}
	return importPath, false
}

// npmPackagePrefix returns the package portion of an npm import
// specifier — the part addressing the package itself, with any
// in-package sub-path dropped. A scoped package keeps its
// `@scope/name`. Returns "" when the specifier carries no sub-path
// (the whole specifier is already the package) so callers can skip a
// redundant lookup:
//
//	"@acme/shared-lib/util" → "@acme/shared-lib"
//	"lodash/get"            → "lodash"
//	"@acme/shared-lib"      → ""   (no sub-path)
//	"lodash"                → ""   (no sub-path)
func npmPackagePrefix(specifier string) string {
	if strings.HasPrefix(specifier, "@") {
		// Scoped: package portion is the first two segments.
		first := strings.IndexByte(specifier, '/')
		if first < 0 {
			return ""
		}
		second := strings.IndexByte(specifier[first+1:], '/')
		if second < 0 {
			return "" // exactly `@scope/name`, no sub-path
		}
		return specifier[:first+1+second]
	}
	if i := strings.IndexByte(specifier, '/'); i >= 0 {
		return specifier[:i]
	}
	return ""
}
