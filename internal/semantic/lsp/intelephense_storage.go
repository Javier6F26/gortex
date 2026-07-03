package lsp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/zzet/gortex/internal/platform"
)

// intelephenseStorageDir returns the per-repo index-cache directory
// intelephense should use, rooted at Gortex's cache home (via
// internal/platform, so XDG_CACHE_HOME overrides win and the path is
// Windows-safe). Without an explicit storagePath intelephense writes its
// index to a default global location outside the isolation every other
// engine artifact honours, and two daemons indexing the same repo path
// would collide there.
func intelephenseStorageDir(repoRoot string) string {
	sum := sha256.Sum256([]byte(repoRoot))
	hash := hex.EncodeToString(sum[:])[:16]
	return filepath.Join(platform.CacheDir(), "intelephense", hash)
}

// intelephenseGlobalStorageDir is the shared, repo-independent cache
// intelephense uses for bundled stubs and cross-repo data.
func intelephenseGlobalStorageDir() string {
	return filepath.Join(platform.CacheDir(), "intelephense", "global")
}

// intelephenseInitOptions builds the initializationOptions that pin
// intelephense's index caches inside Gortex's cache home. It is resolved at
// initialize time (not a baked literal) because the path depends on the
// per-user cache root and the indexed repo root. Best-effort MkdirAll so the
// server can open the caches on first launch; errors are non-fatal.
func intelephenseInitOptions(repoRoot string) json.RawMessage {
	storage := intelephenseStorageDir(repoRoot)
	global := intelephenseGlobalStorageDir()
	_ = os.MkdirAll(storage, 0o755)
	_ = os.MkdirAll(global, 0o755)
	opts := struct {
		StoragePath       string `json:"storagePath"`
		GlobalStoragePath string `json:"globalStoragePath"`
	}{StoragePath: storage, GlobalStoragePath: global}
	b, err := json.Marshal(opts)
	if err != nil {
		return nil
	}
	return json.RawMessage(b)
}
