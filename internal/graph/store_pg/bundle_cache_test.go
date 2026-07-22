package store_pg

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

// newTestBundleCache builds an empty cache in the same shape Open wires it,
// so the tests exercise the real lookup/store/refresh methods.
func newTestBundleCache() *bundleCache {
	return &bundleCache{
		fingerprints: map[string]uint64{},
		entries:      map[string]*bundleCacheEntry{},
	}
}

func sampleBundles(pkg string) []graph.SymbolBundle {
	return []graph.SymbolBundle{{
		Node: &graph.Node{ID: pkg + "::Sym", FilePath: pkg + "/f.go"},
	}}
}

// An un-armed cache (no fingerprints ever installed) fails closed: store is
// a no-op and lookup always misses. This is the fingerprint-0 staleness hole
// closed by follower-analysis-gate — a follower never installs fingerprints,
// so its cache must never serve a bundle that could outlive a writer update.
func TestBundleCache_FailsClosedUntilArmed(t *testing.T) {
	c := newTestBundleCache()

	c.store("pkg/a", sampleBundles("pkg/a"))
	if _, ok := c.lookup("pkg/a"); ok {
		t.Fatal("un-armed cache served a bundle; it must fail closed until fingerprints are installed")
	}
	if len(c.entries) != 0 {
		t.Fatalf("un-armed store must be a no-op; got %d entries", len(c.entries))
	}
}

// An empty fingerprint map does not arm the cache — arming requires a
// non-empty map (a real analysis pass). A follower, which never runs
// analysis, therefore stays permanently un-armed.
func TestBundleCache_EmptyFingerprintsDoNotArm(t *testing.T) {
	c := newTestBundleCache()

	c.refresh(map[string]uint64{})
	if c.armed {
		t.Fatal("empty fingerprint map must not arm the cache")
	}
	c.store("pkg/a", sampleBundles("pkg/a"))
	if _, ok := c.lookup("pkg/a"); ok {
		t.Fatal("cache armed by an empty fingerprint map served a bundle")
	}
}

// Once fingerprints are installed the cache serves fresh entries and
// invalidates on fingerprint change — the pre-existing writer behavior,
// preserved.
func TestBundleCache_ServesAndInvalidatesWhenArmed(t *testing.T) {
	c := newTestBundleCache()
	c.refresh(map[string]uint64{"pkg/a": 1})
	if !c.armed {
		t.Fatal("non-empty fingerprint map must arm the cache")
	}

	c.store("pkg/a", sampleBundles("pkg/a"))
	if _, ok := c.lookup("pkg/a"); !ok {
		t.Fatal("armed cache should serve a stored entry at the current fingerprint")
	}

	// Writer moves the package fingerprint → the cached entry is stale.
	c.refresh(map[string]uint64{"pkg/a": 2})
	if _, ok := c.lookup("pkg/a"); ok {
		t.Fatal("entry must be invalidated when its package fingerprint changes")
	}
}
