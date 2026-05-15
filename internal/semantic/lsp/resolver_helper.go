package lsp

import (
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ResolverHelper adapts a *Provider for resolve-time use by the
// cross-file resolver. The resolver consults this helper as part of
// the hot path for every TS/JS/JSX/TSX edge (see
// internal/resolver/lsp_resolve.go). Compared to the enricher path
// (Provider.Enrich), the helper holds the language server warm
// across the whole resolve pass, serialises calls so tsserver's
// finicky concurrency model can't deadlock, and applies a per-call
// timeout so a stalled server never gates the resolve.
//
// Lifecycle:
//   - Constructed once per (workspace, language family) at index time.
//   - Lazy-spawns the underlying LSP subprocess on first call.
//   - Caches no answers across passes — the resolver owns dedup
//     via its lspIndex.
//
// Safe for concurrent calls; Definition serialises against the
// underlying client so tsserver sees one definition request at a
// time. Callers (the resolver's parallel workers) block on the
// helper mutex; the trade-off is precision over throughput in TS
// families, which is the N5 contract.
type ResolverHelper struct {
	// providerOnce gates lazy spawn so the underlying LSP server
	// isn't started until the first Definition call lands.
	providerOnce sync.Once
	providerErr  error

	// provider may be set at construction (eager) or resolved
	// lazily via providerFn (router-backed). Reads after
	// providerOnce.Do are race-free.
	provider   *Provider
	providerFn func() (*Provider, error)

	workspaceRoot string

	// extensions is the set of lowercase file extensions (with
	// leading dot) the helper claims. Populated from the spec at
	// construction time so SupportsPath can short-circuit without
	// touching the provider lock.
	extensions map[string]struct{}

	// timeout caps each textDocument/definition call. tsserver
	// usually answers in <100 ms on warm buffers, but a cold
	// project load can take seconds. 1500 ms is a conservative
	// per-call budget: long enough for typical warm answers,
	// short enough that the parallel resolver doesn't stall on a
	// genuinely-broken server.
	timeout time.Duration

	// mu serialises Definition calls. tsserver / typescript-
	// language-server tolerate concurrent requests poorly on cold
	// buffers; serialising trades throughput for correctness.
	mu sync.Mutex

	logger *zap.Logger
}

// NewResolverHelper wraps a Provider for resolve-time use. The
// helper claims every extension the underlying spec declares (when
// the provider was constructed from a ServerSpec); otherwise it
// claims the TS-family extensions by default, matching the N5
// initial scope.
//
// workspaceRoot is the absolute path the LSP server is initialised
// against. timeout caps each definition call; pass 0 to apply the
// default (1500 ms).
func NewResolverHelper(provider *Provider, workspaceRoot string, timeout time.Duration, logger *zap.Logger) *ResolverHelper {
	if logger == nil {
		logger = zap.NewNop()
	}
	if timeout <= 0 {
		timeout = 1500 * time.Millisecond
	}
	if abs, err := filepath.Abs(workspaceRoot); err == nil {
		workspaceRoot = abs
	}

	exts := make(map[string]struct{})
	if provider != nil && provider.spec != nil {
		for _, e := range provider.spec.Extensions {
			exts[strings.ToLower(e)] = struct{}{}
		}
	}
	if len(exts) == 0 {
		// Default TS-family scope — matches N5 initial coverage.
		for _, e := range []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"} {
			exts[e] = struct{}{}
		}
	}

	h := &ResolverHelper{
		provider:      provider,
		workspaceRoot: workspaceRoot,
		extensions:    exts,
		timeout:       timeout,
		logger:        logger,
	}
	// Pre-fire providerOnce since the provider is already concrete.
	h.providerOnce.Do(func() {})
	return h
}

// NewLazyResolverHelper builds a helper whose underlying *Provider
// is resolved on first use via lookup(). This is the router-backed
// flavour — pass a closure that calls Router.ForSpecWorkspace or
// equivalent. lookup() runs at most once across the helper's
// lifetime (subsequent failures sticky); concurrent first-use calls
// see the same result.
//
// extensions narrows the set of file extensions the helper claims
// before lookup() fires. Pass nil to use the default TS-family set
// (matching N5 scope).
func NewLazyResolverHelper(lookup func() (*Provider, error), workspaceRoot string, extensions []string, timeout time.Duration, logger *zap.Logger) *ResolverHelper {
	if logger == nil {
		logger = zap.NewNop()
	}
	if timeout <= 0 {
		timeout = 1500 * time.Millisecond
	}
	if abs, err := filepath.Abs(workspaceRoot); err == nil {
		workspaceRoot = abs
	}

	exts := make(map[string]struct{})
	for _, e := range extensions {
		exts[strings.ToLower(e)] = struct{}{}
	}
	if len(exts) == 0 {
		for _, e := range []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"} {
			exts[e] = struct{}{}
		}
	}

	return &ResolverHelper{
		providerFn:    lookup,
		workspaceRoot: workspaceRoot,
		extensions:    exts,
		timeout:       timeout,
		logger:        logger,
	}
}

// ensureProvider resolves the underlying *Provider, running the
// lazy lookup at most once. Returns the cached error on every
// subsequent call.
func (h *ResolverHelper) ensureProvider() (*Provider, error) {
	h.providerOnce.Do(func() {
		if h.provider != nil || h.providerFn == nil {
			return
		}
		p, err := h.providerFn()
		if err != nil {
			h.providerErr = err
			return
		}
		h.provider = p
	})
	if h.providerErr != nil {
		return nil, h.providerErr
	}
	return h.provider, nil
}

// SupportsPath implements resolver.LSPHelper.
//
// SupportsPath does NOT trigger the lazy provider lookup — it's
// answered purely from the extension set. This keeps the
// short-circuit cheap (no LSP spawn) for the common case where the
// resolver asks "do you handle this file?" against many candidate
// edges, only a fraction of which will actually want a Definition
// call.
func (h *ResolverHelper) SupportsPath(relPath string) bool {
	if h == nil || relPath == "" {
		return false
	}
	if h.provider == nil && h.providerFn == nil {
		return false
	}
	ext := strings.ToLower(filepath.Ext(relPath))
	_, ok := h.extensions[ext]
	return ok
}

// Definition implements resolver.LSPHelper. Returns
// (definitionRelPath, 1-based line, ok).
//
// Implementation notes:
//   - The provider is spawned lazily on first call (EnsureClient).
//   - The file is opened with didOpen on first call (EnsureFileOpen)
//     so tsserver has the buffer in its workspace state.
//   - The identifier column on `oneBasedLine` is resolved from the
//     cached source so the LSP cursor sits on the identifier.
//   - The returned path is repo-relative when possible (matching
//     graph.Node.FilePath), else falls back to absolute.
func (h *ResolverHelper) Definition(relPath string, oneBasedLine int, name string) (string, int, bool) {
	if h == nil {
		return "", 0, false
	}
	if !h.SupportsPath(relPath) {
		return "", 0, false
	}
	if oneBasedLine <= 0 || name == "" {
		return "", 0, false
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	provider, err := h.ensureProvider()
	if err != nil || provider == nil {
		if err != nil {
			h.logger.Debug("resolve-time LSP: provider lookup failed",
				zap.String("path", relPath), zap.Error(err))
		}
		return "", 0, false
	}

	if err := provider.EnsureClient(h.workspaceRoot); err != nil {
		h.logger.Debug("resolve-time LSP: ensure client failed",
			zap.String("path", relPath), zap.Error(err))
		return "", 0, false
	}
	if err := provider.EnsureFileOpen(h.workspaceRoot, relPath); err != nil {
		h.logger.Debug("resolve-time LSP: open document failed",
			zap.String("path", relPath), zap.Error(err))
		return "", 0, false
	}

	src := provider.Source(h.workspaceRoot, relPath)
	col := IdentifierColumn(src, oneBasedLine, name)

	locs, err := provider.FindDefinition(h.workspaceRoot, relPath, oneBasedLine-1, col, h.timeout)
	if err != nil {
		h.logger.Debug("resolve-time LSP: definition error",
			zap.String("path", relPath), zap.Int("line", oneBasedLine),
			zap.String("name", name), zap.Error(err))
		return "", 0, false
	}
	if len(locs) == 0 {
		return "", 0, false
	}

	// First location is the canonical definition. Tsserver may
	// return multiple (e.g. an interface declaration plus its
	// implementations); the resolver picks the first as the
	// "source of truth" and falls through to the heuristic when
	// the kind gate rejects it.
	loc := locs[0]
	abs := uriToAbsLocalPath(loc.URI)
	if abs == "" {
		return "", 0, false
	}
	rel := abs
	if r, err := filepath.Rel(h.workspaceRoot, abs); err == nil {
		// filepath.Rel can produce "../" paths when the
		// definition sits outside the workspace (node_modules
		// resolution, for example). Reject those — the
		// resolver's graph only has nodes for files under the
		// workspace.
		if !strings.HasPrefix(r, "..") {
			rel = filepath.ToSlash(r)
		} else {
			return "", 0, false
		}
	}
	return rel, loc.Range.Start.Line + 1, true
}

// uriToAbsLocalPath converts a file:// URI to an absolute local
// path. Returns "" for non-file URIs or malformed input. Mirrors
// the behaviour of uriToAbsPath but is exported intent-named here
// for clarity in resolver wiring.
func uriToAbsLocalPath(uri string) string {
	if uri == "" {
		return ""
	}
	if strings.HasPrefix(uri, "file://") {
		parsed, err := url.Parse(uri)
		if err != nil {
			return ""
		}
		return parsed.Path
	}
	// Some servers (rare) reply with a bare absolute path.
	if filepath.IsAbs(uri) {
		return uri
	}
	return ""
}

// Close shuts down the underlying provider. Called by the indexer
// at shutdown when the helper owns a dedicated provider; helpers
// borrowing a router-managed provider can skip Close — the router
// owns the lifecycle.
//
// Safe to call when the lazy lookup has not yet fired — Close is a
// no-op in that case.
func (h *ResolverHelper) Close() error {
	if h == nil {
		return nil
	}
	if h.provider == nil {
		return nil
	}
	return h.provider.Close()
}
