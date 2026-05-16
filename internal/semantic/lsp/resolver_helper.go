package lsp

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ResolverHelper adapts one or more *Provider instances for resolve-
// time use by the cross-file resolver. The resolver consults this
// helper as part of the hot path for every TS/JS/JSX/TSX edge (see
// internal/resolver/lsp_resolve.go). Compared to the enricher path
// (Provider.Enrich), the helper holds the language server warm
// across the whole resolve pass and applies a per-call timeout so a
// stalled server never gates the resolve.
//
// Concurrency model: the helper owns a pool of N independent
// providers (= N tsserver processes for the TS spec). Each Definition
// call borrows one provider from the pool, runs FindDefinition, and
// returns it. Within a single provider tsserver is single-threaded,
// but across providers calls are parallel. Pool size 1 collapses to
// the original "one provider, fully serialised" model.
//
// Lifecycle:
//   - Constructed once per (workspace, language family) at index time.
//   - Lazy-spawns N underlying LSP subprocesses on the first Definition
//     call.
//   - Caches no answers across passes — the resolver owns dedup via
//     its lspIndex.
//
// Memory note: each pooled provider opens files independently via
// EnsureFileOpen. For workspaces with thousands of hot source files
// the per-provider state can add up; the pool size knob trades
// throughput for tsserver memory.
type ResolverHelper struct {
	// spawnOnce gates the lazy creation of the provider pool so the
	// underlying LSP processes aren't started until the first
	// Definition call lands.
	spawnOnce sync.Once
	spawnErr  error

	// poolSize is the number of providers to spawn. Zero is treated
	// as 1 (single-provider, mu-serialised) for back-compat with the
	// pre-pool ResolverHelper API.
	poolSize int

	// pool is a buffered channel of borrowable providers. Capacity =
	// poolSize. Definition takes a provider off the channel, uses it,
	// and puts it back. Allocated inside spawnPool under spawnOnce.
	pool chan *Provider

	// providers is the master slice of pooled providers, retained so
	// Close can shut them down without draining the pool channel
	// (which may have providers in flight).
	providers []*Provider

	// spawnFn produces a fresh, initialised *Provider each call. Pool
	// mode calls it poolSize times; legacy single-provider mode uses
	// it as a lookup that returns a cached singleton (called once).
	// At most one of spawnFn and provider is set at construction.
	spawnFn func() (*Provider, error)

	// provider is the pre-supplied provider in eager-construction
	// mode (NewResolverHelper). When set, the pool collapses to size
	// 1 and the channel holds this one entry. Mutually exclusive
	// with spawnFn.
	provider *Provider

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
//
// The pool collapses to size 1 — call NewPooledResolverHelper to get
// the multi-provider parallel mode.
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
		poolSize:      1,
		workspaceRoot: workspaceRoot,
		extensions:    exts,
		timeout:       timeout,
		logger:        logger,
	}
	// Pre-fire spawnOnce since the provider is already concrete: seed
	// the pool channel with the single supplied provider so the first
	// Definition call doesn't try to spawn.
	h.spawnOnce.Do(func() {
		h.pool = make(chan *Provider, 1)
		if provider != nil {
			h.providers = []*Provider{provider}
			h.pool <- provider
		}
	})
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
	return NewPooledResolverHelper(lookup, workspaceRoot, extensions, timeout, 1, logger)
}

// NewPooledResolverHelper builds a helper backed by `poolSize`
// independent providers. Each Definition call borrows one provider
// from the pool, so up to poolSize concurrent tsserver Definition
// requests run in parallel — eliminating the single-mutex throughput
// ceiling that dominated multi-repo resolve-time profiles (29 min
// `deferred_passes_all` on a 488-repo TS-heavy workspace).
//
// spawn must produce a fresh, fully-initialised provider each call.
// For the typical router-backed wiring the closure is something like:
//
//	func() (*Provider, error) {
//	    p := lsp.NewProviderFromSpec(spec, logger)
//	    if err := p.EnsureClient(absRoot); err != nil { return nil, err }
//	    return p, nil
//	}
//
// poolSize ≤ 0 falls back to the default (4) — large enough to
// saturate the resolver's worker pool on commodity CPU counts, small
// enough that the per-workspace tsserver memory footprint stays
// bounded.
func NewPooledResolverHelper(
	spawn func() (*Provider, error),
	workspaceRoot string,
	extensions []string,
	timeout time.Duration,
	poolSize int,
	logger *zap.Logger,
) *ResolverHelper {
	if logger == nil {
		logger = zap.NewNop()
	}
	if timeout <= 0 {
		timeout = 1500 * time.Millisecond
	}
	if poolSize <= 0 {
		poolSize = 4
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
		spawnFn:       spawn,
		poolSize:      poolSize,
		workspaceRoot: workspaceRoot,
		extensions:    exts,
		timeout:       timeout,
		logger:        logger,
	}
}

// ensurePool spawns the pool's providers on first call, populating
// the borrow channel. Subsequent calls are a no-op. Returns the
// cached error when spawn failed — Definition then short-circuits.
func (h *ResolverHelper) ensurePool() error {
	h.spawnOnce.Do(func() {
		// Eager-construction path (NewResolverHelper): the pool was
		// pre-seeded with the supplied provider when the helper was
		// constructed. Nothing more to do.
		if h.spawnFn == nil {
			return
		}
		size := h.poolSize
		if size <= 0 {
			size = 1
		}
		pool := make(chan *Provider, size)
		providers := make([]*Provider, 0, size)
		for i := 0; i < size; i++ {
			p, err := h.spawnFn()
			if err != nil {
				// Spawn failed mid-way — close anything we already
				// got and surface the error. The helper is poisoned
				// for the rest of its lifetime so we don't keep
				// retrying a broken tsserver in the resolver hot path.
				for _, prov := range providers {
					go func(prov *Provider) { _ = prov.Close() }(prov)
				}
				h.spawnErr = err
				return
			}
			providers = append(providers, p)
			pool <- p
		}
		h.providers = providers
		h.pool = pool
		if size > 1 {
			h.logger.Info("resolve-time LSP: provider pool spawned",
				zap.String("workspace", h.workspaceRoot),
				zap.Int("pool_size", size))
		}
	})
	return h.spawnErr
}

// borrow takes a provider out of the pool (blocking until one is
// available) and returns a release closure the caller defers. The
// pool guarantees mutual exclusion per provider — within tsserver
// each provider's stdio is single-threaded — while allowing up to
// poolSize Definition calls to run in parallel across distinct
// providers.
func (h *ResolverHelper) borrow() (*Provider, func()) {
	p := <-h.pool
	return p, func() { h.pool <- p }
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
	if h.provider == nil && h.spawnFn == nil {
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

	if err := h.ensurePool(); err != nil {
		h.logger.Debug("resolve-time LSP: pool spawn failed",
			zap.String("path", relPath), zap.Error(err))
		return "", 0, false
	}
	if h.pool == nil {
		// Eager-construction path was given a nil provider — short-
		// circuit instead of deadlocking on a never-fed channel.
		return "", 0, false
	}

	provider, release := h.borrow()
	defer release()

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

// SpawnProviderForResolver creates a fresh, fully-initialised
// *Provider against the given workspace root for use as one slot in a
// ResolverHelper pool. Unlike Router.ForSpecWorkspace this does NOT
// cache — every call spawns a new tsserver process. Use it as the
// spawnFn argument to NewPooledResolverHelper. The returned provider
// is owned by the helper; helper.Close shuts it down.
func SpawnProviderForResolver(spec *ServerSpec, workspaceRoot string, logger *zap.Logger) (*Provider, error) {
	if spec == nil {
		return nil, fmt.Errorf("nil spec")
	}
	p := NewProviderFromSpec(spec, logger)
	if err := p.EnsureClient(workspaceRoot); err != nil {
		_ = p.Close()
		return nil, err
	}
	return p, nil
}

// ResolverPoolSizeFromEnv returns the pool size for the resolve-time
// LSP hot path, honouring GORTEX_LSP_POOL_SIZE. Falls back to the
// caller's defaultSize when the env var is unset or unparseable.
// Clamped to [1, 32] to keep tsserver memory bounded.
//
// Default is intentionally 1: at one provider per workspace the
// caller's daemon-side wiring can route through Router.ForSpecWorkspace
// (which idle-reaps unused providers via the LSP router's existing
// reaper), keeping the multi-workspace memory footprint at "one
// long-lived tsserver per workspace" — matching the pre-pool design.
// Values >1 spawn N FRESH tsservers per workspace via
// SpawnProviderForResolver, which have NO idle reaping; opt in only
// when the tracked-workspace count is small (rough rule of thumb:
// total_workspaces * pool_size * 150 MB < available RAM).
func ResolverPoolSizeFromEnv(defaultSize int) int {
	if defaultSize <= 0 {
		defaultSize = 1
	}
	raw := os.Getenv("GORTEX_LSP_POOL_SIZE")
	if raw == "" {
		return defaultSize
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return defaultSize
	}
	if n > 32 {
		n = 32
	}
	return n
}

// Close shuts down every provider in the pool. Called by the indexer
// at shutdown when the helper owns its providers; helpers built
// around router-managed providers (NewLazyResolverHelper /
// NewPooledResolverHelper that close over Router.ForSpecWorkspace)
// can still call Close — the underlying Provider.Close is idempotent
// and routers re-spawn on next demand.
//
// Safe to call when the lazy spawn has not yet fired — Close is a
// no-op in that case.
func (h *ResolverHelper) Close() error {
	if h == nil {
		return nil
	}
	var firstErr error
	for _, p := range h.providers {
		if p == nil {
			continue
		}
		if err := p.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
