package semantic

import (
	"context"

	"github.com/zzet/gortex/internal/graph"
)

// Provider enriches graph edges and nodes with semantic information
// from a language-aware analysis backend (SCIP, go/types, LSP, etc.).
type Provider interface {
	// Name returns a human-readable identifier (e.g., "scip-go", "go-types", "gopls").
	Name() string

	// Languages returns the language codes this provider handles (e.g., ["go"]).
	Languages() []string

	// Available reports whether this provider can run. Checks for
	// external tool availability (e.g., scip-go on PATH, go command present).
	Available() bool

	// Enrich performs a full enrichment pass over the graph for the given repo root.
	// It upgrades edge confidence, adds missing edges, and fills Node.Meta fields.
	// Called after tree-sitter indexing + resolver pass completes.
	Enrich(g graph.Store, repoRoot string) (*EnrichResult, error)

	// EnrichFile performs a targeted enrichment for a single file and its
	// immediate dependents. Used in watch mode for incremental updates.
	// Returns nil result if incremental enrichment is not supported.
	EnrichFile(g graph.Store, repoRoot string, filePath string) (*EnrichResult, error)

	// Close releases any resources held by the provider (daemon processes,
	// temp files, connections).
	Close() error
}

// RepoScopedProvider is an optional interface a Provider MAY implement to
// receive the repo prefix of the enrichment root alongside the root path.
// In a multi-repo daemon the shared graph holds file nodes from every
// tracked repo, and two repos can share a relative path; a provider that
// selects its work by walking graph file nodes needs the prefix to scope
// to the repo actually being enriched rather than guessing from disk
// existence (which a path collision defeats). The Manager calls EnrichRepo
// when the provider implements it, passing the repo's prefix (empty in
// single-repo mode); otherwise it falls back to Enrich.
type RepoScopedProvider interface {
	EnrichRepo(g graph.Store, repoPrefix, repoRoot string) (*EnrichResult, error)
}

// ContextEnricher is an optional interface a Provider MAY implement to
// receive a cancellation context for its per-repo pass. Providers that
// implement it are cancelled *cooperatively* at the Manager's per-repo
// deadline instead of being detached: the provider lands whatever work it
// has completed, marks the result Partial, and returns — so a deadline
// never discards finished enrichment and never leaks a goroutine that
// keeps mutating the graph after the pass was "abandoned".
type ContextEnricher interface {
	EnrichRepoContext(ctx context.Context, g graph.Store, repoPrefix, repoRoot string) (*EnrichResult, error)
}

// ReadinessProber is an optional interface a Provider MAY implement when its
// server answers `initialize` quickly but is not ready to serve semantic
// queries until a slower background load finishes — the Roslyn / MSBuild
// solution load a csharp-ls / OmniSharp pass sits behind. The Manager calls
// WaitReady BEFORE it starts the per-repo enrichment deadline, so that cold
// load does not consume the query budget (which otherwise elapses during the
// load, leaving the pass with zero useful edges). WaitReady must respect ctx
// (a bounded readiness budget) and should return promptly for a server that is
// already ready — providers whose servers serve queries immediately after
// initialize simply do not implement this interface. The returned error is
// best-effort: enrichment proceeds regardless, so a probe failure never blocks
// the pass.
type ReadinessProber interface {
	WaitReady(ctx context.Context, repoRoot string) error
}

// EnrichResult contains statistics from an enrichment pass.
type EnrichResult struct {
	Provider        string  `json:"provider"`
	Language        string  `json:"language"`
	EdgesConfirmed  int     `json:"edges_confirmed"`
	EdgesRefuted    int     `json:"edges_refuted"`
	EdgesAdded      int     `json:"edges_added"`
	NodesEnriched   int     `json:"nodes_enriched"`
	SymbolsCovered  int     `json:"symbols_covered"`
	SymbolsTotal    int     `json:"symbols_total"`
	CoveragePercent float64 `json:"coverage_percent"`
	DurationMs      int64   `json:"duration_ms"`
	// Partial reports that the pass was cut short (per-repo deadline /
	// context cancellation) after landing some — but not all — of its
	// work. The counters above reflect only what actually reached the
	// graph. AbortReason carries the cause when Partial is true.
	Partial     bool   `json:"partial,omitempty"`
	AbortReason string `json:"abort_reason,omitempty"`
	// BoundReason states why the add-phase stopped where it did, so a
	// "completed" state that covered < 100% of its targets is never read as
	// full coverage: "budget" (a deadline cut the pass), "cap" (the pass
	// finished but some targets were skipped, e.g. no source position or an
	// unservable file), or "completed_all" (every target visited).
	BoundReason string `json:"bound_reason,omitempty"`
	// ReferencesAddPass reports that the enrich pass added edges via
	// textDocument/references (rather than call hierarchy) because the
	// server advertised references but no call hierarchy — e.g. intelephense.
	// Lets index_health distinguish this add mode from the hierarchy mode.
	ReferencesAddPass bool `json:"references_add_pass,omitempty"`
}

// Bounding reasons for the enrichment add-phase (EnrichResult.BoundReason /
// EnrichmentStatus.BoundReason).
const (
	EnrichBoundBudget       = "budget"
	EnrichBoundCap          = "cap"
	EnrichBoundCompletedAll = "completed_all"
)

// Enrichment lifecycle states surfaced per (repo, provider) via
// Manager.EnrichmentStatuses — the health signal that lets an agent see
// an un-enriched or partially-enriched graph instead of assuming green.
const (
	EnrichStateRunning   = "running"
	EnrichStateCompleted = "completed"
	EnrichStatePartial   = "partial"   // deadline hit; completed work landed and is counted
	EnrichStateAbandoned = "abandoned" // legacy provider detached at deadline; result discarded
	EnrichStateFailed    = "failed"
)

// EnrichmentStatus reports the lifecycle state of one provider's per-repo
// enrichment pass. Exposed through index_health so consumers can tell a
// fully-enriched graph from one whose LSP pass was cut or abandoned.
type EnrichmentStatus struct {
	Repo            string  `json:"repo"`
	Provider        string  `json:"provider"`
	Language        string  `json:"language,omitempty"`
	State           string  `json:"state"`
	DeadlineSeconds float64 `json:"deadline_seconds,omitempty"`
	DurationMs      int64   `json:"duration_ms,omitempty"`
	EdgesConfirmed  int     `json:"edges_confirmed"`
	EdgesAdded      int     `json:"edges_added"`
	NodesEnriched   int     `json:"nodes_enriched"`
	// Add-phase coverage — the targets eligible for the hover/references
	// pass, how many were visited, and why the pass stopped. Always emitted
	// so a "completed" state that covered < 100% of targets is legible as a
	// coverage sliver rather than trusted as full enrichment.
	SymbolsTotal    int     `json:"symbols_total"`
	SymbolsCovered  int     `json:"symbols_covered"`
	CoveragePercent float64 `json:"coverage_percent"`
	BoundReason     string  `json:"bound_reason,omitempty"`
	// ReferencesAddPass marks that this provider added edges via
	// textDocument/references (no call hierarchy) — the intelephense-style
	// add mode. Distinguishes it from the call-hierarchy add mode.
	ReferencesAddPass bool   `json:"references_add_pass,omitempty"`
	Detail            string `json:"detail,omitempty"`
}

// ProviderStatus represents the current state of a semantic provider.
type ProviderStatus struct {
	Name            string        `json:"name"`
	Language        string        `json:"language"`
	Status          string        `json:"status"` // "ready", "unavailable", "error"
	CoveragePercent float64       `json:"coverage_percent,omitempty"`
	LastResult      *EnrichResult `json:"last_result,omitempty"`
}
