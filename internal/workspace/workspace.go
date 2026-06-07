// Package workspace defines the per-project index-directory marker.
//
// A directory that contains a `.gortex/` index directory is a Gortex
// project root. The legacy `.gortex/workspace.toml` workspace-marker
// handshake (Resolve / Bind / Marker) has been removed: multi-repo
// scoping is the daemon's job, resolved per session from the client's
// working directory (see internal/indexer.ScopeForCWD and the MCP
// server's sessionScope), not from a marker file.
package workspace

// IndexDir is the per-project index directory name: a directory that
// contains `.gortex/` is a Gortex project root. `gortex init` creates
// it to hold the repo's `.gortex.yaml` and related config.
const IndexDir = ".gortex"
