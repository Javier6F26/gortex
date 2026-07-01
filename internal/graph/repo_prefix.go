package graph

import "strings"

// RepoPrefixOfID returns the repository prefix encoded in a node ID or
// file path — the leading path segment before the first "/". In
// multi-repo mode every node ID and file path is prefixed with the repo
// it belongs to (e.g. "gortex/internal/mcp/server.go::NewServer" →
// "gortex"), so this recovers the owning repo without a node lookup.
//
// Returns "" for IDs that carry no repo prefix: unresolved sentinels
// like "unresolved::Name", and single-repo-mode IDs that were never
// prefixed. Callers use it only inside a workspace-bound (multi-repo)
// path, where every real node is prefixed, so an empty result simply
// never matches a workspace's repo set.
func RepoPrefixOfID(id string) string {
	if i := strings.IndexByte(id, '/'); i > 0 {
		return id[:i]
	}
	return ""
}
