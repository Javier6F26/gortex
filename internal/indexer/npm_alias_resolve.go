package indexer

import (
	"path"
	"strings"
	"sync"

	"github.com/zzet/gortex/internal/modules"
)

// npmAliasIndex implements resolver.NpmAliasResolver: it rewrites a
// JS/TS import specifier that resolves through an npm alias declared
// in the importing file's nearest-ancestor package.json.
//
// An npm alias declares a dependency under one name while pointing it
// at a different real package:
//
//	"dependencies": { "shared": "npm:@acme/shared-lib@1.4.0" }
//
// `import x from 'shared'` then refers to `@acme/shared-lib`, and
// `import x from 'shared/util'` to `@acme/shared-lib/util`. The
// resolver only knows the bare specifier, so without this rewrite a
// locally-vendored `@acme/shared-lib` is missed and the import falls
// through to an external stub.
//
// The index is read-only after construction apart from its parsed-
// manifest cache, which is guarded by mu because the resolver's
// resolveEdge workers run in parallel.
type npmAliasIndex struct {
	// roots maps a repo prefix to its on-disk root. Entries with an
	// empty prefix model single-repo mode (no prefix on graph paths).
	roots map[string]string

	mu sync.Mutex
	// aliasCache memoises one parsed package.json: disk path → the
	// alias map (dependency key → real package name) for that file.
	// A nil value records "read, but no npm-alias entries / missing
	// file" so a miss is not re-read on every import edge.
	aliasCache map[string]map[string]string
}

// newNpmAliasIndex builds an index over the given repo roots. Returns
// nil when no usable root is supplied — callers treat a nil resolver
// as "no alias rewriting", which is the pre-feature behaviour.
func newNpmAliasIndex(roots map[string]string) *npmAliasIndex {
	usable := make(map[string]string, len(roots))
	for prefix, root := range roots {
		if root != "" {
			usable[prefix] = root
		}
	}
	if len(usable) == 0 {
		return nil
	}
	return &npmAliasIndex{
		roots:      usable,
		aliasCache: map[string]map[string]string{},
	}
}

// Resolve is the resolver.NpmAliasResolver entry point. callerFile is
// the importing file's graph path (repo-prefixed in multi-repo mode);
// specifier is the verbatim import specifier. It returns the specifier
// with its package portion swapped for the npm-alias real name, or ""
// when the specifier is not an npm alias for this importer.
func (x *npmAliasIndex) Resolve(callerFile, specifier string) string {
	if x == nil || callerFile == "" || specifier == "" {
		return ""
	}
	// Only JS/TS imports go through npm aliases. A relative or
	// absolute specifier is a path import, never a package name.
	if !isJSTSFile(callerFile) {
		return ""
	}
	if strings.HasPrefix(specifier, ".") || strings.HasPrefix(specifier, "/") {
		return ""
	}
	root, relDir, ok := x.locate(callerFile)
	if !ok {
		return ""
	}
	pkgName, subPath := splitPackageSpecifier(specifier)
	if pkgName == "" {
		return ""
	}
	// Walk from the importing file's directory up to the repo root,
	// stopping at the first package.json that declares the specifier
	// — npm resolution honours the nearest manifest.
	for dir := relDir; ; dir = path.Dir(dir) {
		aliases := x.aliasesFor(joinPath(root, joinRel(dir, "package.json")))
		if real, found := aliases[pkgName]; found {
			if subPath == "" {
				return real
			}
			return real + "/" + subPath
		}
		if dir == "." || dir == "" || dir == "/" {
			return ""
		}
	}
}

// locate resolves callerFile to (repoRoot, repoRelativeDir). The
// longest matching prefix wins so nested repo roots resolve to the
// most specific one.
func (x *npmAliasIndex) locate(callerFile string) (root, relDir string, ok bool) {
	bestPrefix := ""
	bestRoot := ""
	for prefix, r := range x.roots {
		switch {
		case prefix == "":
			// Single-repo mode: graph paths carry no prefix.
			if bestRoot == "" {
				bestRoot = r
			}
		case callerFile == prefix || strings.HasPrefix(callerFile, prefix+"/"):
			if len(prefix) > len(bestPrefix) {
				bestPrefix = prefix
				bestRoot = r
			}
		}
	}
	if bestRoot == "" {
		return "", "", false
	}
	rel := callerFile
	if bestPrefix != "" {
		rel = strings.TrimPrefix(callerFile, bestPrefix+"/")
	}
	return bestRoot, path.Dir(rel), true
}

// aliasesFor returns the npm-alias map (dependency key → real package
// name) parsed from the package.json at absPath, reading and caching
// it on first request. The result is never nil-returned to callers as
// a map — a missing or alias-free manifest yields an empty map so the
// caller's lookup is a clean miss.
func (x *npmAliasIndex) aliasesFor(absPath string) map[string]string {
	x.mu.Lock()
	defer x.mu.Unlock()
	if cached, seen := x.aliasCache[absPath]; seen {
		return cached
	}
	var aliases map[string]string
	if src, ok := readDiskFile(absPath); ok {
		for _, spec := range modules.ParsePackageJSON(src) {
			if spec.Ecosystem != "npm" || spec.Alias == "" {
				continue
			}
			if aliases == nil {
				aliases = map[string]string{}
			}
			aliases[spec.Path] = spec.Alias
		}
	}
	x.aliasCache[absPath] = aliases
	return aliases
}

// splitPackageSpecifier splits an import specifier into its package
// name and the sub-path within that package. A scoped package keeps
// its `@scope/name` as the package portion:
//
//	"shared"            → ("shared", "")
//	"shared/util"       → ("shared", "util")
//	"@acme/lib"         → ("@acme/lib", "")
//	"@acme/lib/util/x"  → ("@acme/lib", "util/x")
func splitPackageSpecifier(specifier string) (pkgName, subPath string) {
	parts := strings.SplitN(specifier, "/", 4)
	if strings.HasPrefix(specifier, "@") {
		// Scoped: the first two segments form the package name.
		if len(parts) < 2 || parts[0] == "@" || parts[1] == "" {
			return "", ""
		}
		pkgName = parts[0] + "/" + parts[1]
		subPath = strings.TrimPrefix(specifier, pkgName)
		return pkgName, strings.TrimPrefix(subPath, "/")
	}
	pkgName = parts[0]
	subPath = strings.TrimPrefix(specifier, pkgName)
	return pkgName, strings.TrimPrefix(subPath, "/")
}

// joinRel joins a repo-relative directory with a file name, treating
// the repo root (".") as no directory prefix.
func joinRel(dir, name string) string {
	if dir == "." || dir == "" {
		return name
	}
	return dir + "/" + name
}

// isJSTSFile reports whether filePath has a JavaScript/TypeScript
// extension — the only files whose imports resolve through npm.
func isJSTSFile(filePath string) bool {
	switch path.Ext(filePath) {
	case ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts", ".mjs", ".cjs":
		return true
	}
	return false
}
