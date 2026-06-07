package resolver

import (
	"context"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/graph"
)

// RemoteDeclarationProber is satisfied by the daemon's Federator. It asks
// each enabled remote whether it owns a declaration matching name,
// passing the caller's import/module hint so a bare-name probe is never
// issued. Returns the first positive hit or ok=false. The
// implementation lives in internal/daemon (keeps internal/resolver pure —
// no HTTP here); it bounds its own per-remote deadline (ProxyToolCtx).
type RemoteDeclarationProber interface {
	ProbeDeclaration(ctx context.Context, name, importHint string) (RemoteDecl, bool)
}

// RemoteDecl is a remote daemon's confirmed declaration of a symbol.
type RemoteDecl struct {
	Slug        string // owning remote roster slug
	RemoteID    string // <prefix>/<file>::<sym> on the remote
	Kind        graph.NodeKind
	RepoPrefix  string
	WorkspaceID string
	File        string
	Line        int
}

// EnableRemoteStitch wires the proxy-edge mint path: a prober and the
// proxy-node heap bound. Called by the daemon entry point only when
// federation.edges.enabled. A nil prober (or budget <= 0 with no prober)
// leaves the resolver in its default read-only fan-out behaviour.
func (cr *CrossRepoResolver) EnableRemoteStitch(prober RemoteDeclarationProber, proxyBudget int) {
	cr.prober = prober
	cr.proxyBudget = proxyBudget
	cr.edgesEnabled = prober != nil
}

// tryRemoteStitch is the gated proxy-edge mint. It runs only after local
// resolution fails (the caller checks e.To == oldTo). On a confirmed
// remote declaration it mints an origin-namespaced proxy node and
// rewrites the edge to it with honest provenance (text_matched, never
// lsp_resolved). Returns true when it stitched.
func (cr *CrossRepoResolver) tryRemoteStitch(e *graph.Edge, name string, stats *CrossRepoStats) bool {
	// 1. EVIDENCE GATE: never mint on a bare name. The caller
	// file must import something for a remote target to be plausible.
	importHint := cr.importHintFor(e)
	if importHint == "" {
		return false
	}
	decl, ok := cr.prober.ProbeDeclaration(context.Background(), name, importHint)
	if !ok {
		return false
	}

	// 2. MINT the proxy node (origin-namespaced so it can never alias a
	// local id), bounded by the heap budget.
	pid := graph.ProxyNodeID(decl.Slug, decl.RemoteID)
	if cr.graph.GetNode(pid) == nil {
		if cr.proxyBudgetExceeded() {
			if cr.logger != nil {
				cr.logger.Warn("federation: proxy-node budget exceeded; mint refused",
					zap.Int("budget", cr.proxyBudget), zap.String("name", name))
			}
			return false
		}
		cr.graph.AddNode(&graph.Node{
			ID:          pid,
			Kind:        decl.Kind,
			Name:        name,
			FilePath:    decl.File,
			StartLine:   decl.Line,
			RepoPrefix:  decl.RepoPrefix,
			WorkspaceID: decl.WorkspaceID,
			Origin:      "remote:" + decl.Slug,
			Stub:        true,
			FetchedAt:   time.Now(),
		})
	}

	// 3. REWRITE the edge to the proxy with honest provenance.
	e.To = pid
	e.CrossRepo = true
	e.Origin = graph.OriginTextMatched
	// resolveFunctionCall counted this edge as Unresolved; it is now a
	// cross-repo edge to the proxy.
	if stats.Unresolved > 0 {
		stats.Unresolved--
	}
	stats.CrossRepoEdges++
	return true
}

// importHintFor returns a comma-joined list of the caller file's import
// targets, or "" when the file imports nothing. A non-empty hint is the
// positive evidence the mint path requires; "" disables the probe.
func (cr *CrossRepoResolver) importHintFor(e *graph.Edge) string {
	fileID := cr.callerFileID(e)
	if fileID == "" {
		return ""
	}
	var hints []string
	seen := map[string]struct{}{}
	for _, ie := range cr.graph.GetOutEdges(fileID) {
		if ie.Kind != graph.EdgeImports {
			continue
		}
		h := importHintName(ie.To)
		if h == "" {
			continue
		}
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		hints = append(hints, h)
	}
	if len(hints) == 0 {
		return ""
	}
	sort.Strings(hints)
	return strings.Join(hints, ",")
}

// importHintName normalises an import edge's target into a module/path
// hint, stripping the unresolved + import:: markers.
func importHintName(to string) string {
	n := graph.UnresolvedName(to)
	if n == "" {
		n = to
	}
	return strings.TrimPrefix(n, "import::")
}

// proxyBudgetExceeded reports whether the graph already holds the maximum
// number of proxy nodes. Counts on demand; mints are rare
// (gated behind the evidence rule + the off-by-default flag).
func (cr *CrossRepoResolver) proxyBudgetExceeded() bool {
	if cr.proxyBudget <= 0 {
		return false
	}
	count := 0
	for _, n := range cr.graph.AllNodes() {
		if graph.IsProxyNode(n) {
			count++
			if count >= cr.proxyBudget {
				return true
			}
		}
	}
	return false
}
