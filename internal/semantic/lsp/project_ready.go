package lsp

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// tsProjectReady reports whether a TypeScript/JavaScript workspace has its
// dependency tree installed. typescript-language-server (and tsgo) resolve
// every import and cross-file type through node_modules; without it, a
// project that has a tsconfig answers "no package metadata" to every request,
// so a whole-repo hover / call-hierarchy sweep runs for minutes and enriches
// nothing (observed: 627 .ts files, 1,703s, zero nodes enriched). The gate is
// intentionally narrow: it only reports not-ready when the repo declares a
// TypeScript project (a tsconfig.json) AND no node_modules exists anywhere in
// it. A repo with loose .js and no tsconfig is left alone — tsserver's
// inferred project still types same-file symbols there.
//
// The walk is directory-only and bounded (depth 5, skipping node_modules /
// VCS / build output), so it is cheap even on a large tree, and it runs once
// per repo per pass, not per file.
func tsProjectReady(root string) (bool, string) {
	if root == "" {
		return true, ""
	}
	var hasTSConfig, hasNodeModules bool
	rootDepth := strings.Count(filepath.Clean(root), string(os.PathSeparator))
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // unreadable entries just don't contribute evidence
		}
		if hasNodeModules && hasTSConfig {
			return fs.SkipAll
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules":
				hasNodeModules = true
				return fs.SkipDir
			case ".git", ".hg", ".svn", "vendor", "dist", "build", ".next", "out", "target":
				return fs.SkipDir
			}
			if strings.Count(filepath.Clean(path), string(os.PathSeparator))-rootDepth >= 5 {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() == "tsconfig.json" {
			hasTSConfig = true
		}
		return nil
	})
	if hasTSConfig && !hasNodeModules {
		return false, "TypeScript project found but node_modules is absent; run `npm install` (or pnpm/yarn), then re-index"
	}
	return true, ""
}
