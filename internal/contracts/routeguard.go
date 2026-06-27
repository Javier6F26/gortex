package contracts

import (
	"path"
	"strings"
)

// IsLikelyHTTPRouteLiteral reports whether a string literal passed to a call
// (with the given callee function name) is plausibly an HTTP route/path rather
// than a filesystem path, config filename, or unrelated string.
//
// It is a deliberately conservative heuristic used to cut false-positive
// "routes" mined from arbitrary string literals. The intuition: a rooted path
// that does not look like a filesystem location, a config file, or the argument
// of a string-manipulation helper is treated as a route. The rule order below
// is intentional — earlier rules are stronger signals and short-circuit the
// later ones:
//
//  1. an http:// or https:// URL is always a route;
//  2. any other URI scheme (file://, s3://, ...) is never a route;
//  3. routes are rooted paths — a literal that does not start with "/" is out;
//  4. a string/path-manipulation callee means the literal is a fragment, not a route;
//  5. filesystem-root-prefixed paths (/etc, /var, /Users, ...) are out;
//  6. hidden config dirs/files (.ssh, .aws, ...) anywhere in the path are out;
//  7. filesystem-y file extensions are out (servable formats only when an API
//     marker is present);
//  8. anything else that survived is treated as a route.
//
// The lookup sets are package-level so the classifier stays cheap and the
// vocabulary is easy to extend.
func IsLikelyHTTPRouteLiteral(literal, calleeName string) bool {
	s := strings.TrimSpace(literal)
	lower := strings.ToLower(s)

	// (1) A real URL is unambiguously a route target.
	if strings.Contains(lower, "http://") || strings.Contains(lower, "https://") {
		return true
	}
	// (2) Any other URI scheme (file://, s3://, postgres://, ...) is not a route.
	if strings.Contains(s, "://") {
		return false
	}
	// (3) Routes are rooted paths; bare filenames and home-relative paths are not.
	if !strings.HasPrefix(s, "/") {
		return false
	}
	// (4) A string-manipulation callee means the literal is a path fragment.
	if isStringManipulationCallee(calleeName) {
		return false
	}

	segments := strings.Split(strings.TrimPrefix(s, "/"), "/")

	// (5) Filesystem-root-prefixed paths (/etc, /var, /Users, ...) are not routes.
	// Match the first segment exactly, so /etcd or /userspace is left alone.
	if filesystemRootSegments[segments[0]] {
		return false
	}
	// (6) Hidden config dirs/files (.ssh, .aws, .kube, ...) anywhere in the path.
	for _, seg := range segments {
		if hiddenConfigSegments[seg] {
			return false
		}
	}

	// (7) Filesystem-y file extensions.
	ext := strings.ToLower(path.Ext(s))
	if filesystemExtensions[ext] {
		return false
	}
	if conditionalExtensions[ext] {
		// Servable formats (.json, .yaml, ...) count as routes only when the
		// path also carries an HTTP API marker (/api, /v1, /graphql, ...).
		return hasAPIMarker(s)
	}

	// (8) A rooted path caught by none of the reject rules looks like a route.
	return true
}

// isStringManipulationCallee reports whether calleeName is a string/path
// joining or splitting helper — split, join, os.path.join, path.join,
// filepath.Join, path.Join. The bare method/function name is matched
// case-insensitively, so qualified forms collapse to their final segment.
func isStringManipulationCallee(calleeName string) bool {
	name := calleeName
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	return stringManipulationCallees[strings.ToLower(name)]
}

// hasAPIMarker reports whether the path carries a segment that strongly implies
// it is served over HTTP rather than read off disk.
func hasAPIMarker(s string) bool {
	for _, marker := range apiMarkerSubstrings {
		if strings.Contains(s, marker) {
			return true
		}
	}
	// A version segment like /v1, /v2, /v10 is a strong API signal.
	for _, seg := range strings.Split(s, "/") {
		if len(seg) >= 2 && seg[0] == 'v' && isAllDigits(seg[1:]) {
			return true
		}
	}
	return false
}

// isAllDigits reports whether s is non-empty and consists only of ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// stringManipulationCallees collapses the qualified path/string helpers
// (os.path.join, filepath.Join, path.Join, ...) to the bare final-segment name
// the callee normaliser produces.
var stringManipulationCallees = map[string]bool{
	"split": true,
	"join":  true,
}

// filesystemRootSegments are first-path-segment names that mark a filesystem
// location rather than an HTTP route.
var filesystemRootSegments = map[string]bool{
	"etc":     true,
	"root":    true,
	"var":     true,
	"usr":     true,
	"home":    true,
	"tmp":     true,
	"private": true,
	"opt":     true,
	"bin":     true,
	"sbin":    true,
	"dev":     true,
	"proc":    true,
	"sys":     true,
	"run":     true,
	"lib":     true,
	"mnt":     true,
	"boot":    true,
	"srv":     true,
	"Users":   true,
	"Volumes": true,
}

// hiddenConfigSegments are dotfile dirs/files that mark local config/state.
var hiddenConfigSegments = map[string]bool{
	".aws":    true,
	".ssh":    true,
	".kube":   true,
	".git":    true,
	".env":    true,
	".docker": true,
	".config": true,
	".npm":    true,
	".cache":  true,
}

// filesystemExtensions always disqualify a literal from being a route.
var filesystemExtensions = map[string]bool{
	".cfg":    true,
	".conf":   true,
	".crt":    true,
	".db":     true,
	".env":    true,
	".ini":    true,
	".key":    true,
	".pem":    true,
	".sock":   true,
	".sqlite": true,
	".toml":   true,
}

// conditionalExtensions can still be routes when an API marker is present.
var conditionalExtensions = map[string]bool{
	".json": true,
	".yaml": true,
	".yml":  true,
	".xml":  true,
}

// apiMarkerSubstrings are path fragments that imply an HTTP-served endpoint.
var apiMarkerSubstrings = []string{
	"/api",
	"/apis",
	"/graphql",
	"/health",
	"/metrics",
}
