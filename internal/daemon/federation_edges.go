package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/indexer"
	"github.com/zzet/gortex/internal/resolver"
)

// WireRemoteStitch activates cross-daemon proxy-edge minting end-to-end
// when the edges feature is enabled and the router carries a federator: it
// installs the evidence prober on the MultiIndexer (so cross-repo
// resolution mints proxy edges) and returns a ProxyHydrator for the read
// path. Returns nil (a no-op) when the feature is off, the router has no
// federator, or there is no MultiIndexer — leaving the daemon on the
// read-only fan-out only.
func WireRemoteStitch(router *Router, mi *indexer.MultiIndexer, g graph.Store, cfg config.FederationEdgesConfig, logger *zap.Logger) *ProxyHydrator {
	if !cfg.IsEnabled() || router == nil || router.federator == nil || mi == nil || g == nil {
		return nil
	}
	remotes := func() []ServerEntry { return router.EffectiveEnabledRemotes(nil) }
	timeout := router.federator.cfg.PerRemoteTimeout

	prober := NewProxyEdgeProber(router.federator, remotes, timeout, logger)
	mi.SetRemoteStitch(prober, cfg.MaxNodes())

	return NewProxyHydrator(g, router.federator.clientFor, remotes,
		cfg.TTL(), cfg.Depth(), cfg.MaxNodes(), timeout, logger)
}

// ProxyEdgeProber implements resolver.RemoteDeclarationProber for the
// proxy-edge mint path: it asks each enabled remote that advertises the
// `subgraph` capability whether it owns a declaration of `name`, via the
// existing find_declaration tool over POST /v1/tools/find_declaration.
// It reuses the Federator's shared client cache, health cache, and
// circuit breaker, so it inherits the bounded-deadline + breaker
// protection of the read-only fan-out.
type ProxyEdgeProber struct {
	fed     *Federator
	remotes func() []ServerEntry // enabled-remote snapshot
	timeout time.Duration
	logger  *zap.Logger
}

// NewProxyEdgeProber wires the prober to the Federator's plumbing and an
// enabled-remote snapshot. Constructed by the daemon entry point only
// when federation.edges.enabled.
func NewProxyEdgeProber(fed *Federator, remotes func() []ServerEntry, timeout time.Duration, logger *zap.Logger) *ProxyEdgeProber {
	if logger == nil {
		logger = zap.NewNop()
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &ProxyEdgeProber{fed: fed, remotes: remotes, timeout: timeout, logger: logger}
}

// ProbeDeclaration asks each subgraph-capable enabled remote whether it
// owns a declaration of name, returning the first positive hit (cheapest,
// deterministic by roster order). importHint is already the positive
// evidence the resolver required to call us at all; the remote
// confirmation is the second half.
func (p *ProxyEdgeProber) ProbeDeclaration(ctx context.Context, name, importHint string) (resolver.RemoteDecl, bool) {
	if p == nil || p.fed == nil || name == "" || importHint == "" {
		return resolver.RemoteDecl{}, false
	}
	body, _ := json.Marshal(map[string]any{"use_site": name})

	for _, rem := range p.remotes() {
		if p.fed.breaker.isOpen(rem.Slug) {
			continue
		}
		cli, err := p.fed.clientFor(rem)
		if err != nil {
			continue
		}
		// Only probe remotes that advertise the subgraph capability;
		// otherwise proxy-edge minting is skipped for this remote and
		// the read path stays on the read-only fan-out.
		h, herr := p.fed.health.get(ctx, cli, p.timeout)
		if herr != nil || !h.HasCapability("subgraph") {
			continue
		}

		rctx, cancel := context.WithTimeout(ctx, p.timeout)
		out, status, err := cli.ProxyToolCtx(rctx, "find_declaration", body)
		cancel()
		if err != nil || status != http.StatusOK {
			p.fed.breaker.fail(rem.Slug)
			continue
		}
		if decl, ok := parseRemoteDecl(out, rem.Slug, name); ok {
			return decl, true
		}
	}
	return resolver.RemoteDecl{}, false
}

// parseRemoteDecl unwraps a find_declaration tool result and returns the
// first declaration whose Name matches name (a real declaration of the
// symbol, not a coincidental use site), mapped to a resolver.RemoteDecl.
func parseRemoteDecl(out []byte, slug, name string) (resolver.RemoteDecl, bool) {
	toolJSON, _ := unwrapToolJSON(out)
	var payload struct {
		Declarations []struct {
			Declaration *graph.Node `json:"declaration"`
		} `json:"declarations"`
	}
	if err := json.Unmarshal(toolJSON, &payload); err != nil {
		return resolver.RemoteDecl{}, false
	}
	for _, g := range payload.Declarations {
		d := g.Declaration
		if d == nil || d.Name != name {
			continue
		}
		return resolver.RemoteDecl{
			Slug:        slug,
			RemoteID:    d.ID,
			Kind:        d.Kind,
			RepoPrefix:  d.RepoPrefix,
			WorkspaceID: d.WorkspaceID,
			File:        d.FilePath,
			Line:        d.StartLine,
		}, true
	}
	return resolver.RemoteDecl{}, false
}

// remoteSubGraph mirrors server.SubGraphResponse on the wire (a local
// copy avoids a daemon -> server import).
type remoteSubGraph struct {
	Root  *graph.Node   `json:"root"`
	Nodes []*graph.Node `json:"nodes"`
	Edges []*graph.Edge `json:"edges"`
	Stats struct {
		FetchedAt time.Time `json:"fetched_at"`
		Truncated bool      `json:"truncated"`
	} `json:"stats"`
}

// GetSubGraph fetches a node's FULL neighbour ring from a remote's
// GET /v1/subgraph (proxy-edge hydration). depth defaults to 1.
func (c *ServerClient) GetSubGraph(ctx context.Context, id string, depth int) (*remoteSubGraph, error) {
	base, err := url.JoinPath(c.BaseURL, "v1", "subgraph")
	if err != nil {
		return nil, fmt.Errorf("join subgraph URL: %w", err)
	}
	q := url.Values{}
	q.Set("id", id)
	if depth > 0 {
		q.Set("depth", strconv.Itoa(depth))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("build subgraph request: %w", err)
	}
	if tok := c.resolveAuthToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch subgraph from %q: %w", c.Entry.Slug, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("subgraph from %q: status %d", c.Entry.Slug, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read subgraph response: %w", err)
	}
	var out remoteSubGraph
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode subgraph from %q: %w", c.Entry.Slug, err)
	}
	return &out, nil
}

// ProxyHydrator lazily fills a proxy node's neighbour ring from the
// owning remote's /v1/subgraph. It lives in the daemon read path (not in
// graph.Graph, which has no HTTP knowledge); --oneshot and pure-local
// installs never construct one.
type ProxyHydrator struct {
	graph     graph.Store
	clientFor func(ServerEntry) (*ServerClient, error)
	remotes   func() []ServerEntry
	ttl       time.Duration
	depth     int
	budget    int
	timeout   time.Duration
	logger    *zap.Logger
}

// NewProxyHydrator builds a hydrator. Constructed by the daemon entry
// point only when federation.edges.enabled.
func NewProxyHydrator(g graph.Store, clientFor func(ServerEntry) (*ServerClient, error), remotes func() []ServerEntry, ttl time.Duration, depth, budget int, timeout time.Duration, logger *zap.Logger) *ProxyHydrator {
	if logger == nil {
		logger = zap.NewNop()
	}
	if depth <= 0 {
		depth = 1
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &ProxyHydrator{
		graph: g, clientFor: clientFor, remotes: remotes,
		ttl: ttl, depth: depth, budget: budget, timeout: timeout, logger: logger,
	}
}

// Hydrate pulls one neighbour ring for a proxy node over /v1/subgraph,
// mints any newly-referenced proxy nodes (origin-namespaced), adds the
// edges with honest provenance, refreshes FetchedAt, and returns the
// number of edges added. No-op when the ring is fresh (within ttl) and
// already populated. Bounded by ctx and the proxy budget.
func (h *ProxyHydrator) Hydrate(ctx context.Context, proxyID string) (int, error) {
	if h == nil || h.graph == nil {
		return 0, nil
	}
	n := h.graph.GetNode(proxyID)
	if n == nil || !graph.IsProxyNode(n) {
		return 0, nil
	}
	if !n.FetchedAt.IsZero() && time.Since(n.FetchedAt) < h.ttl &&
		len(h.graph.GetOutEdges(proxyID)) > 0 {
		return 0, nil
	}

	slug := graph.ProxyOriginSlug(proxyID)
	remoteID := graph.ProxyRemoteID(proxyID)
	rem, ok := h.remoteForSlug(slug)
	if !ok {
		return 0, nil
	}
	cli, err := h.clientFor(rem)
	if err != nil {
		return 0, err
	}
	rctx, cancel := context.WithTimeout(ctx, h.timeout)
	sg, err := cli.GetSubGraph(rctx, remoteID, h.depth)
	cancel()
	if err != nil {
		return 0, err
	}

	// Mint a proxy node for each neighbour (origin-namespaced).
	for _, rn := range sg.Nodes {
		if rn == nil || rn.ID == "" || rn.ID == remoteID {
			continue
		}
		pid := graph.ProxyNodeID(slug, rn.ID)
		if h.graph.GetNode(pid) != nil {
			continue
		}
		if h.budgetExceeded() {
			h.logger.Warn("federation: proxy budget exceeded during hydration",
				zap.String("slug", slug))
			break
		}
		h.graph.AddNode(&graph.Node{
			ID: pid, Kind: rn.Kind, Name: rn.Name,
			FilePath: rn.FilePath, StartLine: rn.StartLine,
			RepoPrefix: rn.RepoPrefix, WorkspaceID: rn.WorkspaceID,
			Origin: "remote:" + slug, Stub: true, FetchedAt: time.Now(),
		})
	}

	// Add the ring's edges, rewriting remote ids to proxy ids (the root
	// maps back to the existing proxy id). Skip an edge whose endpoint we
	// did not pull (it would dangle).
	added := 0
	for _, re := range sg.Edges {
		if re == nil {
			continue
		}
		from := h.proxyize(slug, re.From, remoteID, proxyID)
		to := h.proxyize(slug, re.To, remoteID, proxyID)
		if h.graph.GetNode(from) == nil || h.graph.GetNode(to) == nil {
			continue
		}
		h.graph.AddEdge(&graph.Edge{
			From: from, To: to, Kind: re.Kind,
			Origin: graph.OriginTextMatched, CrossRepo: true,
		})
		added++
	}

	// Refresh the root proxy's FetchedAt (AddNode upserts).
	refreshed := *n
	refreshed.FetchedAt = time.Now()
	h.graph.AddNode(&refreshed)
	return added, nil
}

// EvictRemote marks every proxy node owned by slug stale (resets
// FetchedAt) so the next access re-hydrates against fresh remote data.
// Called on a graph_invalidated frame from that remote. The
// graph.Store has no node-removal primitive that targets the origin
// namespace cleanly, so staleness is expressed as a forced re-hydrate
// rather than a hard delete — same observable outcome (fresh data on the
// next read). Returns the number of proxy nodes invalidated.
func (h *ProxyHydrator) EvictRemote(slug string) int {
	if h == nil || h.graph == nil || slug == "" {
		return 0
	}
	count := 0
	for _, n := range h.graph.AllNodes() {
		if !graph.IsProxyNode(n) || graph.ProxyOriginSlug(n.ID) != slug {
			continue
		}
		stale := *n
		stale.FetchedAt = time.Time{}
		h.graph.AddNode(&stale)
		count++
	}
	return count
}

func (h *ProxyHydrator) proxyize(slug, remoteNodeID, rootRemoteID, rootProxyID string) string {
	if remoteNodeID == rootRemoteID {
		return rootProxyID
	}
	return graph.ProxyNodeID(slug, remoteNodeID)
}

func (h *ProxyHydrator) remoteForSlug(slug string) (ServerEntry, bool) {
	for _, r := range h.remotes() {
		if r.Slug == slug {
			return r, true
		}
	}
	return ServerEntry{}, false
}

func (h *ProxyHydrator) budgetExceeded() bool {
	if h.budget <= 0 {
		return false
	}
	count := 0
	for _, n := range h.graph.AllNodes() {
		if graph.IsProxyNode(n) {
			count++
			if count >= h.budget {
				return true
			}
		}
	}
	return false
}
