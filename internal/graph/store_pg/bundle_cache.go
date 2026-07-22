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
	// armed is true only after SetBundleFingerprints has installed a
	// non-empty fingerprint map. Until then the cache fails closed: lookup
	// always misses and store is a no-op. This closes the fingerprint-0
	// staleness hole — without arming, an entry stored at fingerprint 0
	// (the zero value for a package with no reported fingerprint) would
	// compare equal to the current 0 forever and serve stale bundles that
	// outlive writer updates. A follower never installs fingerprints (it
	// runs no analysis pass — see follower-analysis-gate), so its cache
	// stays permanently un-armed and can never serve a stale bundle.
	// store_sqlite already has this discipline via its per-entry
	// "no fingerprint → don't cache" guard; this aligns store_pg with it.
	armed bool
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

	// Fail closed until fingerprints have been installed — an un-armed
	// cache can never validate an entry, so it must never serve one.
	if !c.armed {
		return nil, false
	}

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
	// Arm only when a non-empty fingerprint map is installed. An empty map
	// (or a nil that was normalised above) leaves the cache un-armed so it
	// keeps failing closed.
	c.armed = len(fps) > 0
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

	// Fail closed until armed: an entry stored before any fingerprint is
	// installed would be tagged fingerprint 0 and could never be
	// distinguished from a stale one, so refuse to cache it.
	if !c.armed {
		return
	}

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
