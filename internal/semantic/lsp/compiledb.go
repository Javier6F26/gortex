package lsp

import (
	"path/filepath"
	"strings"
)

// hasCompileDB reports whether root carries a clangd-usable compilation
// database, or an explicit clangd configuration that means the user
// wired the server up deliberately. Without one, clangd falls back to a
// per-file preamble + AST rebuild on every didOpen — the churn the
// degraded enrichment path exists to avoid.
//
// Probed, in order:
//   - <root>/compile_commands.json        — the canonical CMake / Bear output
//   - <root>/build*/compile_commands.json — out-of-tree build directories
//   - <root>/compile_flags.txt            — clangd's flat-flags fallback
//   - <root>/.clangd                      — user opted clangd in by hand
//
// This mirrors the indexer's compile-DB probe without importing it: the
// import graph runs indexer→semantic only, so a helper both need must be
// stdlib-only and live on this side of the boundary. No caching — one
// stat set runs per enrichment pass.
func hasCompileDB(root string) bool {
	if root == "" {
		return false
	}
	if fileExists(filepath.Join(root, "compile_commands.json")) {
		return true
	}
	if matches, _ := filepath.Glob(filepath.Join(root, "build*", "compile_commands.json")); len(matches) > 0 {
		return true
	}
	if fileExists(filepath.Join(root, "compile_flags.txt")) {
		return true
	}
	if fileExists(filepath.Join(root, ".clangd")) {
		return true
	}
	return false
}

// isCXXHeaderFile reports whether rel names a C / C++ header. A degraded
// (no-compile-database) pass skips headers: opening one directly makes
// clangd treat it as a standalone translation unit and rebuild a full
// fallback AST for a file that, without a database, offers no cross-file
// signal in return.
func isCXXHeaderFile(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".h", ".hh", ".hpp", ".hxx", ".h++":
		return true
	}
	return false
}
