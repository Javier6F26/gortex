package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/zzet/gortex/internal/platform"
)

// SavedScope is a named, persisted set of repository prefixes — a reusable
// slice of a multi-repo workspace that query tools accept by name instead
// of re-specifying repo filters on every call.
type SavedScope struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Repos       []string `json:"repos"`
	// Paths narrows a scope below the repository grain to a set of
	// sub-paths -- the monorepo-service slice (services/billing,
	// libs/auth). Anchored, slash-segment-normalised prefixes
	// relative to each repo root. Empty (the back-compatible default)
	// means the scope is repo-level only -- an existing scopes.json
	// without this field decodes cleanly.
	Paths []string `json:"paths,omitempty"`
}

// scopeStore is a small JSON-file-backed registry of SavedScopes. It
// survives daemon restarts. All exported methods are safe for concurrent
// use.
type scopeStore struct {
	mu     sync.Mutex
	path   string
	byName map[string]SavedScope
}

// scopesFilePath returns the on-disk location of the saved-scope store,
// honouring GORTEX_SCOPES_PATH (used by tests) over the cache default.
//
// An absolute $XDG_CACHE_HOME wins; otherwise the store stays under
// os.UserCacheDir() — the historical location, kept so an existing
// scopes file is not orphaned.
func scopesFilePath() string {
	if p := strings.TrimSpace(os.Getenv("GORTEX_SCOPES_PATH")); p != "" {
		return p
	}
	return filepath.Join(platform.OSCacheDir(), "scopes.json")
}

// newScopeStore builds a store at path and loads any persisted scopes.
func newScopeStore(path string) *scopeStore {
	st := &scopeStore{path: path, byName: map[string]SavedScope{}}
	st.load()
	return st
}

// load reads persisted scopes; a missing or unreadable file leaves the
// store empty. Called only from the constructor, so it takes no lock.
func (st *scopeStore) load() {
	data, err := os.ReadFile(st.path)
	if err != nil {
		return
	}
	var scopes []SavedScope
	if json.Unmarshal(data, &scopes) != nil {
		return
	}
	for _, sc := range scopes {
		if sc.Name != "" {
			st.byName[sc.Name] = sc
		}
	}
}

// save persists the store. Callers hold st.mu.
func (st *scopeStore) save() error {
	scopes := make([]SavedScope, 0, len(st.byName))
	for _, sc := range st.byName {
		scopes = append(scopes, sc)
	}
	sort.Slice(scopes, func(i, j int) bool { return scopes[i].Name < scopes[j].Name })
	data, err := json.MarshalIndent(scopes, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(st.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(st.path, data, 0o644)
}

func (st *scopeStore) get(name string) (SavedScope, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	sc, ok := st.byName[name]
	return sc, ok
}

func (st *scopeStore) list() []SavedScope {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]SavedScope, 0, len(st.byName))
	for _, sc := range st.byName {
		out = append(out, sc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (st *scopeStore) put(sc SavedScope) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.byName[sc.Name] = sc
	return st.save()
}

func (st *scopeStore) remove(name string) (bool, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.byName[name]; !ok {
		return false, nil
	}
	delete(st.byName, name)
	return true, st.save()
}

// scopeStoreOrInit lazily constructs the per-server saved-scope store.
func (s *Server) scopeStoreOrInit() *scopeStore {
	s.scopesOnce.Do(func() {
		s.scopes = newScopeStore(scopesFilePath())
	})
	return s.scopes
}

// lookupScope returns the named saved scope.
func (s *Server) lookupScope(name string) (SavedScope, bool) {
	return s.scopeStoreOrInit().get(name)
}

// scopeRepoSet expands a saved scope into a repo-prefix allow-set.
func (s *Server) scopeRepoSet(sc SavedScope) map[string]bool {
	out := make(map[string]bool, len(sc.Repos))
	for _, r := range sc.Repos {
		if r = strings.TrimSpace(r); r != "" {
			out[r] = true
		}
	}
	return out
}
