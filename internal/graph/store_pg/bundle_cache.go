package store_pg

import (
	"path/filepath"
	"sync"

	"github.com/zzet/gortex/internal/graph"
)

// bundleCache is a content-addressed package-scoped cache for
// SearchSymbolBundles. A cache entry stores the search result for a package
// whose content fingerprint is unchanged; staleness is detected by comparing
// the per-package content fingerprint supplied by the daemon's analysis pass
// against the fingerprint stored when the entry was created.
//
// PostgresSQL's SearchSymbolBundles is already fast because it operates
// through pg_trgm + batched GetInEdgesByNodeIDs/GetOutEdgesByNodeIDs, so
// the cache is a secondary optimization for the hottest packages. It mirrors
// the store_sqlite bundle_cache design exactly.
type bundleCache struct {
	mu           sync.Mutex
	fingerprints map[string]uint64
	entries      map[string]*bundleCacheEntry
}

type bundleCacheEntry struct {
	fingerprint uint64
	bundles     []graph.SymbolBundle
}

func (c *bundleCache) lookup(pkgKey string) ([]graph.SymbolBundle, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[pkgKey]
	if !ok {
		return nil, false
	}
	currentFP := c.fingerprints[pkgKey]
	if entry.fingerprint != currentFP {
		// Stale — remove and return miss.
		delete(c.entries, pkgKey)
		return nil, false
	}
	return entry.bundles, true
}

// refresh swaps in the new fingerprint map and prunes every entry whose
// package fingerprint no longer matches. Called by SetBundleFingerprints
// after each analysis pass.
func (c *bundleCache) refresh(fps map[string]uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if fps == nil {
		fps = map[string]uint64{}
	}
	c.fingerprints = fps
	for pkgKey, entry := range c.entries {
		if c.fingerprints[pkgKey] != entry.fingerprint {
			delete(c.entries, pkgKey)
		}
	}
}

func (c *bundleCache) store(pkgKey string, bundles []graph.SymbolBundle) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[pkgKey] = &bundleCacheEntry{
		fingerprint: c.fingerprints[pkgKey],
		bundles:     bundles,
	}
}

// bundlePackageKey derives the package key for a node's file path. It
// mirrors the analysis layer's packageKey so the cache and the
// daemon-supplied fingerprint map agree on package identity: the
// directory the file lives in (repo-prefixed in multi-repo because the
// stored file paths are), or "" for a file at the repo root / a node
// with no path.
func bundlePackageKey(filePath string) string {
	if filePath == "" {
		return ""
	}
	dir := filepath.Dir(filepath.ToSlash(filePath))
	if dir == "." {
		return ""
	}
	return dir
}
