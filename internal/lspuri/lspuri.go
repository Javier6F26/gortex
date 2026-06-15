// Package lspuri converts between filesystem paths and file:// URIs
// correctly on both POSIX and Windows.
//
// PURPOSE — the naive `"file://" + path` concatenation is correct on
// POSIX (an absolute path already starts with `/`, yielding the required
// three slashes) but BROKEN on Windows: `C:\repo\Main.java` becomes
// `file://C:\repo\Main.java` — the drive letter lands in the authority,
// backslashes are not URI separators, and the round-trip back to a path
// drops the drive. The net effect on Windows is a silent no-op: an LSP
// server (jdtls, etc.) cannot match the document we open, and the URIs it
// returns fail to map back to a repo path, so every definition/reference/
// implementation result is discarded and no edge is ever enriched.
//
// RATIONALE — centralise the conversion so didOpen, position requests,
// and result fold-back all agree on URI shape, and so the Windows-specific
// drive-letter / separator handling lives (and is tested) in exactly one
// place rather than being re-derived at each call site.
//
// KEYWORDS — file-uri, windows, drive-letter, lsp, path-conversion
package lspuri

import (
	"net/url"
	"path/filepath"
	"strings"
)

// PathToURI converts a filesystem path to a file:// URI.
//
//	POSIX:   /repo/Main.java   -> file:///repo/Main.java
//	Windows: C:\repo\Main.java -> file:///C:/repo/Main.java
//
// Relative paths are made absolute first. Returns "" for an empty path.
func PathToURI(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return slashAbsToURI(filepath.ToSlash(abs))
}

// URIToAbsPath converts a file:// URI to an OS-native absolute path.
//
//	file:///C:/repo/Main.java -> C:\repo\Main.java (Windows) / C:/repo/Main.java (POSIX runtime)
//	file:///repo/Main.java    -> /repo/Main.java
//
// Returns "" for non-file or unparseable URIs. URL-encoded octets
// (e.g. %20) are decoded.
func URIToAbsPath(uri string) string {
	slash := uriToSlashAbs(uri)
	if slash == "" {
		return ""
	}
	return filepath.FromSlash(slash)
}

// URIToRepoRel converts a file:// URI to a forward-slash path relative to
// repoRoot, or "" when the URI is outside repoRoot or unparseable.
//
// Uses filepath.Rel (case-insensitive on Windows) rather than a string
// prefix so drive-letter casing and separator differences never cause a
// false miss — the failure mode that made LSP fold-back silently drop
// every result on Windows.
func URIToRepoRel(uri, repoRoot string) string {
	abs := URIToAbsPath(uri)
	if abs == "" || repoRoot == "" {
		return ""
	}
	rel, err := filepath.Rel(repoRoot, abs)
	if err != nil {
		return ""
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "" // outside the repo
	}
	return filepath.ToSlash(rel)
}

// slashAbsToURI builds a file URI from a forward-slash absolute path.
// Pure (no filepath / OS dependency) so it is deterministically testable
// for both POSIX ("/repo/x") and Windows-drive ("C:/repo/x") shapes.
func slashAbsToURI(slashAbs string) string {
	if slashAbs == "" {
		return ""
	}
	if !strings.HasPrefix(slashAbs, "/") {
		slashAbs = "/" + slashAbs // C:/repo -> /C:/repo so the URI gets file:///C:/repo
	}
	return (&url.URL{Scheme: "file", Path: slashAbs}).String()
}

// uriToSlashAbs extracts the forward-slash absolute path from a file URI.
// Pure; testable for both shapes. Strips the spurious leading slash that
// precedes a Windows drive letter (/C:/.. -> C:/..).
func uriToSlashAbs(uri string) string {
	if uri == "" {
		return ""
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	if parsed.Scheme != "" && parsed.Scheme != "file" {
		return ""
	}
	p := parsed.Path
	if p == "" {
		p = parsed.Opaque // tolerate `file:relative` shapes
	}
	if len(p) >= 3 && p[0] == '/' && isDriveLetter(p[1]) && p[2] == ':' {
		p = p[1:] // /C:/repo -> C:/repo
	}
	return p
}

func isDriveLetter(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}
