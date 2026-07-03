package mcp

import (
	"path"
	"strings"

	"github.com/zzet/gortex/internal/graph"
)

// Barrel re-export resolve-through for find_usages.
//
// A JS/TS barrel forwards a binding it does not declare —
// `export { persist } from './middleware/persist'` — and the extractor records
// that only as an EdgeReExports edge from the barrel FILE; it mints no node for
// the forwarded binding. So a consumer's public import path
// (`src/middleware.ts::persist`) has nothing to resolve and find_usages returns
// not_found even though the canonical declaration has usages.
//
// reExportBindingCanonical maps such a barrel-binding id to the canonical
// declaration id by walking the re-export chain (named, aliased, and chained
// barrels, plus `export * from`). It is consulted ONLY when the queried id is
// not itself a node, so it can never change the answer for a real symbol — the
// canonical strata stay byte-identical. Bare / path-aliased specifiers (needing
// tsconfig `paths` or npm-alias resolution the query layer does not carry) are
// out of scope; relative barrels — the dominant form — are covered.

// jsBarrelExts are the module file extensions probed when resolving a relative
// re-export specifier to a file node, in precedence order.
var jsBarrelExts = []string{".ts", ".tsx", ".mts", ".cts", ".d.ts", ".js", ".jsx", ".mjs", ".cjs"}

const maxReExportChainDepth = 8

// reExportBindingCanonical returns the canonical declaration id a barrel-binding
// id forwards to, or "" when id is not a resolvable barrel binding.
func reExportBindingCanonical(g graph.Store, id string, depth int) string {
	if g == nil || depth > maxReExportChainDepth {
		return ""
	}
	barrelFile, name, ok := splitBarrelID(id)
	if !ok || !hasJSTSExt(barrelFile) {
		return ""
	}
	if g.GetNode(barrelFile) == nil {
		return ""
	}
	for _, e := range g.GetOutEdges(barrelFile) {
		if e == nil || e.Kind != graph.EdgeReExports {
			continue
		}
		spec, orig := parseReExportTarget(e.To)
		if spec == "" || !strings.HasPrefix(spec, ".") {
			continue // bare / aliased specifier — out of scope
		}
		switch {
		case orig != "":
			// Named (optionally aliased) re-export. The public binding name is
			// the alias when renamed, else the original.
			binding := e.Alias
			if binding == "" {
				binding = orig
			}
			if binding != name {
				continue
			}
			if canon := probeBindingInFile(g, barrelFile, spec, orig, depth); canon != "" {
				return canon
			}
		case e.Alias == "":
			// `export * from './x'` — any of x's exports is forwarded under its
			// own name, so probe the target for the queried name.
			if canon := probeBindingInFile(g, barrelFile, spec, name, depth); canon != "" {
				return canon
			}
		}
	}
	return ""
}

// probeBindingInFile resolves the relative spec to a target file and returns
// the canonical id for `orig` there — either a real symbol node, or, when the
// target is itself a barrel, the result of recursing through it.
func probeBindingInFile(g graph.Store, fromFile, spec, orig string, depth int) string {
	tf := probeRelativeModuleFile(g, fromFile, spec)
	if tf == "" {
		return ""
	}
	if canon := tf + "::" + orig; g.GetNode(canon) != nil {
		return canon
	}
	return reExportBindingCanonical(g, tf+"::"+orig, depth+1)
}

// splitBarrelID splits `<file>::<name>` on the last `::`.
func splitBarrelID(id string) (file, name string, ok bool) {
	i := strings.LastIndex(id, "::")
	if i < 0 {
		return "", "", false
	}
	return id[:i], id[i+2:], true
}

// parseReExportTarget decodes an EdgeReExports edge's target
// (`unresolved::import::<spec>[::<orig>]`) into its import specifier and, for a
// named re-export, the original binding name ("" for a wildcard / namespace).
func parseReExportTarget(to string) (spec, orig string) {
	payload, ok := strings.CutPrefix(graph.UnresolvedName(to), "import::")
	if !ok || payload == "" {
		return "", ""
	}
	if i := strings.LastIndex(payload, "::"); i >= 0 {
		return payload[:i], payload[i+2:]
	}
	return payload, ""
}

// probeRelativeModuleFile joins a relative specifier against the importing
// file's directory and returns the matching file node's id, trying the module
// extensions and an index file, or "" when no such file node exists.
func probeRelativeModuleFile(g graph.Store, fromFile, spec string) string {
	stem := path.Clean(path.Join(path.Dir(fromFile), spec))
	for _, ext := range jsBarrelExts {
		if g.GetNode(stem+ext) != nil {
			return stem + ext
		}
	}
	for _, ext := range jsBarrelExts {
		if cand := path.Join(stem, "index"+ext); g.GetNode(cand) != nil {
			return cand
		}
	}
	return ""
}

// hasJSTSExt reports whether a file path is a JS/TS module the barrel walk
// applies to.
func hasJSTSExt(file string) bool {
	for _, ext := range jsBarrelExts {
		if strings.HasSuffix(file, ext) {
			return true
		}
	}
	return false
}
