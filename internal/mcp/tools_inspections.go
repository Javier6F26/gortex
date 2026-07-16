package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/zzet/gortex/internal/analysis"
	"github.com/zzet/gortex/internal/graph"
)

// registerInspectionsTools wires list_inspections + run_inspections.
//
// These two tools form the substrate the future D5 JetBrains plugin
// will surface as "run inspections" / "list inspections" in the IDE
// — same MCP-tool name as serena's JetBrains-only equivalent, but
// powered by gortex graph analyzers + LSP diagnostics + guards +
// contracts. Works today without any IDE plugin.
//
// Each inspection has:
//   - id: stable identifier ("dead_code", "cycles", "todos", ...)
//   - category: grouping for IDE display ("dead-code", "complexity",
//     "concurrency", "guards", "contracts")
//   - description: one-line summary
//   - severity: default severity emitted for matches
//   - run(...): the inspector implementation returning uniform
//     violation rows
//
// New inspections plug in by adding an entry to the registry below.
func (s *Server) registerInspectionsTools() {
	s.addTool(
		mcp.NewTool("list_inspections",
			mcp.WithDescription("Return the catalog of available inspections. Each entry has {id, category, description, severity}. Use as a discovery call before run_inspections to learn which inspector IDs exist on this server. Surface targeted by the D5 JetBrains plugin (when shipped) and consumable today by any agent that wants a structured view of what gortex can detect."),
			mcp.WithString("category", mcp.Description("Filter to a single category (e.g. dead-code, complexity, concurrency, guards, contracts). Empty = return all.")),
			mcp.WithString("format", mcp.Description("Output format: json (default), gcx, or toon")),
		),
		s.handleListInspections,
	)

	s.addTool(
		mcp.NewTool("run_inspections",
			mcp.WithDescription("Run one or more inspections and return uniform violation rows: {inspection, severity, file, line, message, symbol_id?}. Set `inspections=all` to run every inspector, or pass a CSV of ids from list_inspections. Aggregated under a summary {by_inspection, total_violations} so the caller can present a punch list. Composes existing analyzers (dead_code, cycles, todos, unsafe_patterns, coverage_gaps, stale_code), guards, and contract checks — no JetBrains dependency."),
			mcp.WithString("inspections", mcp.Description("Comma-separated inspection IDs to run, or `all` for the full catalog. Required.")),
			mcp.WithString("path_prefix", mcp.Description("Scope every inspector to nodes under this file-path prefix.")),
			mcp.WithString("severity", mcp.Description("Filter results to this severity (error / warning / info). Empty = no filter.")),
			mcp.WithNumber("max_per_inspection", mcp.Description("Cap on violations per inspector (default: 50).")),
			mcp.WithString("format", mcp.Description("Output format: json (default), gcx, or toon")),
		),
		s.handleRunInspections,
	)
}

// inspectionViolation is the wire row every inspector emits.
type inspectionViolation struct {
	Inspection string `json:"inspection"`
	Severity   string `json:"severity"`
	File       string `json:"file,omitempty"`
	Line       int    `json:"line,omitempty"`
	Message    string `json:"message"`
	SymbolID   string `json:"symbol_id,omitempty"`
}

// inspectionSpec is the registry entry. run returns the inspector's
// violations bounded by the scope predicate (which may be nil for
// "no filter").
type inspectionSpec struct {
	ID          string
	Category    string
	Description string
	Severity    string
	Run         func(s *Server, scope inspectionScope) []inspectionViolation
	// Available, when non-nil, gates the inspection: it reports whether
	// the inspector's data source is present and, if not, a human reason.
	// An unavailable inspection is reported as skipped rather than run —
	// so a registry-dependent inspector on a follower with no contracts
	// surfaces "skipped: registry unavailable" instead of a silent
	// zero-violation clean that is indistinguishable from a real pass.
	Available func(s *Server) (ok bool, reason string)
}

// inspectionScope is the call-side filter passed to every inspector.
// Keep it minimal — adding fields here forces every inspector to
// consider the new dimension.
type inspectionScope struct {
	PathPrefix string
}

// inspect returns true if path is in scope.
func (sc inspectionScope) keep(path string) bool {
	if sc.PathPrefix == "" {
		return true
	}
	return strings.HasPrefix(path, sc.PathPrefix)
}

// inspectionRegistry is the single source of truth — list_inspections
// projects this; run_inspections looks up by id.
func inspectionRegistry() []inspectionSpec {
	return []inspectionSpec{
		{
			ID: "dead_code", Category: "dead-code", Severity: "warning",
			Description: "Functions/methods with zero incoming references (excluding test files, CGo exports, and entry-point heuristics).",
			Run:         runDeadCodeInspection,
		},
		{
			ID: "cycles", Category: "complexity", Severity: "warning",
			Description: "Circular dependency chains in the import / call graph.",
			Run:         runCyclesInspection,
		},
		{
			ID: "todos", Category: "documentation", Severity: "info",
			Description: "TODO / FIXME / HACK / XXX / NOTE comments extracted as KindTodo nodes.",
			Run:         runTodosInspection,
		},
		{
			ID: "coverage_gaps", Category: "testing", Severity: "info",
			Description: "Symbols with meta.coverage_pct < 100 (requires `gortex enrich coverage` to have populated the graph).",
			Run:         runCoverageGapsInspection,
		},
		{
			ID: "stale_code", Category: "maintenance", Severity: "info",
			Description: "Symbols whose meta.last_authored is older than 365 days (requires `gortex enrich blame`).",
			Run:         runStaleCodeInspection,
		},
		{
			ID: "guards", Category: "guards", Severity: "error",
			Description: "Project-specific guard rules — co-change and boundary violations evaluated against the scoped node set.",
			Run:         runGuardsInspection,
		},
		{
			ID: "contracts_orphans", Category: "contracts", Severity: "warning",
			Description: "Provider/consumer contracts with no matching counterpart in the active workspace.",
			Run:         runContractOrphansInspection,
			Available: func(s *Server) (bool, string) {
				reg := s.effectiveContractRegistry()
				if reg == nil {
					return false, "contract registry unavailable — no contracts indexed or persisted in this store"
				}
				// Registry-health gate: a zero-entry registry while the store
				// still holds kind=contract nodes means hydration failed. Report
				// skipped rather than run — otherwise every contract falsely reads
				// as "provider with no counterpart" (the 100%-orphan failure mode).
				if len(reg.All()) == 0 && s.storeHasContractNodes() {
					return false, "contract registry hydrated empty while the store holds contract nodes — orphan results would be untrustworthy"
				}
				return true, ""
			},
		},
	}
}

func (s *Server) handleListInspections(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	categoryFilter := strings.TrimSpace(req.GetString("category", ""))
	rows := []map[string]any{}
	for _, spec := range inspectionRegistry() {
		if categoryFilter != "" && spec.Category != categoryFilter {
			continue
		}
		rows = append(rows, map[string]any{
			"id":          spec.ID,
			"category":    spec.Category,
			"description": spec.Description,
			"severity":    spec.Severity,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i]["id"].(string) < rows[j]["id"].(string)
	})
	return s.respondJSONOrTOON(ctx, req, map[string]any{
		"inspections": rows,
		"total":       len(rows),
	})
}

func (s *Server) handleRunInspections(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	idsArg, err := req.RequireString("inspections")
	if err != nil {
		return mcp.NewToolResultError("`inspections` is required (CSV of ids, or `all`)"), nil
	}
	scope := inspectionScope{
		PathPrefix: strings.TrimSpace(req.GetString("path_prefix", "")),
	}
	severity := strings.ToLower(strings.TrimSpace(req.GetString("severity", "")))
	maxPer := max(req.GetInt("max_per_inspection", 50), 1)

	all := inspectionRegistry()
	want := map[string]bool{}
	if strings.TrimSpace(idsArg) == "all" {
		for _, sp := range all {
			want[sp.ID] = true
		}
	} else {
		for _, id := range splitCSV(idsArg) {
			want[id] = true
		}
	}

	results := []map[string]any{}
	for _, sp := range all {
		if !want[sp.ID] {
			continue
		}
		// An inspection whose data source is absent is reported skipped —
		// never a silent zero-violation clean that reads as a real pass.
		if sp.Available != nil {
			if ok, reason := sp.Available(s); !ok {
				results = append(results, map[string]any{
					"inspection": sp.ID,
					"category":   sp.Category,
					"severity":   sp.Severity,
					"violations": []inspectionViolation{},
					"total":      0,
					"returned":   0,
					"truncated":  false,
					"skipped":    true,
					"reason":     reason,
				})
				continue
			}
		}
		raw := sp.Run(s, scope)
		// Apply the severity filter, then cap at max_per_inspection. `total`
		// is the TRUE post-filter count; `returned` is how many rows the
		// block actually carries. Keeping both means the summary (built
		// from these blocks below) can never claim violations that aren't
		// accounted for by a present, truncation-marked block.
		filtered := make([]inspectionViolation, 0, len(raw))
		for _, v := range raw {
			if severity != "" && strings.ToLower(v.Severity) != severity {
				continue
			}
			filtered = append(filtered, v)
		}
		total := len(filtered)
		returned := filtered
		truncated := false
		if len(returned) > maxPer {
			returned = returned[:maxPer]
			truncated = true
		}
		results = append(results, map[string]any{
			"inspection": sp.ID,
			"category":   sp.Category,
			"severity":   sp.Severity,
			"violations": returned,
			"total":      total,
			"returned":   len(returned),
			"truncated":  truncated,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i]["inspection"].(string) < results[j]["inspection"].(string)
	})

	// max_per_inspection is this tool's truncation control, so the default
	// response-size budget MUST NOT silently drop whole result blocks (it
	// would leave the summary counting violations no block carries — the
	// cap=200 regression). Honour only an EXPLICIT max_bytes/max_tokens, and
	// then by trimming violations WITHIN blocks so every counted inspection
	// stays present and truncation-marked.
	if budget, explicit := explicitInspectionBudget(req); explicit && budget > 0 {
		trimInspectionResultsToBudget(results, budget)
	}

	// Build the summary from the emitted blocks — never from a running
	// counter tallied before response assembly. This makes block/summary
	// divergence structurally impossible at every cap and budget value.
	return s.respondJSONOrTOONNoBudget(ctx, req, map[string]any{
		"results":            results,
		"summary":            summarizeInspectionResults(results),
		"path_prefix":        scope.PathPrefix,
		"max_per_inspection": maxPer,
	})
}

// explicitInspectionBudget reports the caller-supplied response byte budget
// and whether one was actually supplied. When neither max_bytes nor
// max_tokens is present the default budget does not apply to
// run_inspections (see handleRunInspections). A present-but-opted-out axis
// resolves to budget 0, which callers treat as "no cap".
func explicitInspectionBudget(req mcp.CallToolRequest) (int, bool) {
	args := req.GetArguments()
	_, hasBytes := numArgInt(args, "max_bytes")
	_, hasTokens := numArgInt(args, "max_tokens")
	if !hasBytes && !hasTokens {
		return 0, false
	}
	return effectiveBudget(req), true
}

// trimInspectionResultsToBudget shrinks the per-block violation arrays until
// the marshalled results fit maxBytes, always leaving every block present
// (with its true `total`) and marking any block it trimmed `truncated`. It
// never drops a whole inspection — that is what desynced the summary before.
func trimInspectionResultsToBudget(results []map[string]any, maxBytes int) {
	// Reserve headroom for the summary + scaffolding the payload wraps the
	// results in, so the whole response (not just the results slice) fits.
	budget := maxBytes - 1024
	if budget < maxBytes/2 {
		budget = maxBytes / 2
	}
	for {
		b, err := json.Marshal(results)
		if err != nil || len(b) <= budget {
			return
		}
		// Halve the block currently carrying the most violations.
		idx, most := -1, 0
		for i, r := range results {
			v, _ := r["violations"].([]inspectionViolation)
			if len(v) > most {
				most, idx = len(v), i
			}
		}
		if idx < 0 || most == 0 {
			return // nothing left to trim
		}
		r := results[idx]
		v := r["violations"].([]inspectionViolation)
		r["violations"] = v[:len(v)/2]
		r["returned"] = len(v) / 2
		r["truncated"] = true
	}
}

// summarizeInspectionResults derives the summary from the emitted blocks.
// `total_violations` / `by_inspection` report each inspection's TRUE count;
// `returned_violations` reports how many rows the response actually carries.
// Skipped inspections are excluded so a skipped block never reads as a clean
// zero-violation pass.
func summarizeInspectionResults(results []map[string]any) map[string]any {
	byInspection := map[string]int{}
	returnedTotal := 0
	totalViolations := 0
	anyTruncated := false
	for _, r := range results {
		if skipped, _ := r["skipped"].(bool); skipped {
			continue
		}
		id, _ := r["inspection"].(string)
		total, _ := r["total"].(int)
		returned, _ := r["returned"].(int)
		byInspection[id] = total
		totalViolations += total
		returnedTotal += returned
		if tr, _ := r["truncated"].(bool); tr {
			anyTruncated = true
		}
	}
	return map[string]any{
		"by_inspection":       byInspection,
		"total_violations":    totalViolations,
		"returned_violations": returnedTotal,
		"truncated":           anyTruncated,
	}
}

// --- Inspector implementations --------------------------------------

func runDeadCodeInspection(s *Server, scope inspectionScope) []inspectionViolation {
	entries := analysis.FindDeadCode(s.graph, s.getProcesses(), nil)
	out := make([]inspectionViolation, 0, len(entries))
	for _, e := range entries {
		if !scope.keep(e.FilePath) {
			continue
		}
		out = append(out, inspectionViolation{
			Inspection: "dead_code",
			Severity:   "warning",
			File:       e.FilePath,
			Line:       e.Line,
			SymbolID:   e.ID,
			Message:    "dead code: " + e.Kind + " " + e.Name + " has zero incoming references",
		})
	}
	return out
}

func runCyclesInspection(s *Server, scope inspectionScope) []inspectionViolation {
	cycles := analysis.DetectCycles(s.graph, s.getCommunities(), "")
	out := make([]inspectionViolation, 0, len(cycles))
	for _, c := range cycles {
		// Cycle path is a list of node IDs; surface the first as the
		// anchor and roll the chain into the message so the agent
		// sees the loop without an extra round-trip.
		chain := strings.Join(c.Path, " → ")
		anchor := ""
		anchorFile := ""
		anchorLine := 0
		if len(c.Path) > 0 {
			anchor = c.Path[0]
			if n := s.graph.GetNode(anchor); n != nil {
				if !scope.keep(n.FilePath) {
					continue
				}
				anchorFile = n.FilePath
				anchorLine = n.StartLine
			}
		}
		out = append(out, inspectionViolation{
			Inspection: "cycles",
			Severity:   "warning",
			File:       anchorFile,
			Line:       anchorLine,
			SymbolID:   anchor,
			Message:    "dependency cycle: " + chain,
		})
	}
	return out
}

func runTodosInspection(s *Server, scope inspectionScope) []inspectionViolation {
	out := make([]inspectionViolation, 0)
	for _, n := range s.graph.AllNodes() {
		if n.Kind != graph.KindTodo {
			continue
		}
		if !scope.keep(n.FilePath) {
			continue
		}
		tag, _ := n.Meta["tag"].(string)
		text, _ := n.Meta["text"].(string)
		msg := tag
		if text != "" {
			if msg != "" {
				msg += ": "
			}
			msg += text
		}
		if msg == "" {
			msg = "todo"
		}
		out = append(out, inspectionViolation{
			Inspection: "todos",
			Severity:   "info",
			File:       n.FilePath,
			Line:       n.StartLine,
			SymbolID:   n.ID,
			Message:    msg,
		})
	}
	return out
}

func runCoverageGapsInspection(s *Server, scope inspectionScope) []inspectionViolation {
	out := make([]inspectionViolation, 0)
	covRows := s.coverageByID()
	for _, n := range s.graph.AllNodes() {
		if n.Kind != graph.KindFunction && n.Kind != graph.KindMethod {
			continue
		}
		if !scope.keep(n.FilePath) {
			continue
		}
		pct, ok := coveragePctFrom(covRows, n)
		if !ok || pct >= 100.0 {
			continue
		}
		out = append(out, inspectionViolation{
			Inspection: "coverage_gaps",
			Severity:   "info",
			File:       n.FilePath,
			Line:       n.StartLine,
			SymbolID:   n.ID,
			Message:    "coverage gap: " + n.Name + " at " + formatPct(pct),
		})
	}
	return out
}

// staleInspectionDays mirrors analyze stale_code's default threshold: a
// function/method whose latest blame authorship is older than this is
// surfaced as a stale_code inspection.
const staleInspectionDays = 365

func runStaleCodeInspection(s *Server, scope inspectionScope) []inspectionViolation {
	out := make([]inspectionViolation, 0)
	now := time.Now().Unix()
	cutoff := now - staleInspectionDays*24*3600
	blame := blameRowsByID(s.graph)
	for _, n := range s.graph.AllNodes() {
		if n.Kind != graph.KindFunction && n.Kind != graph.KindMethod {
			continue
		}
		if !scope.keep(n.FilePath) {
			continue
		}
		// last_authored is a nested map (commit / email / timestamp),
		// primarily from the blame sidecar and falling back to the
		// node's meta — lastAuthoredFrom normalises both. Reading it as
		// a bare string (as this inspection once did) always missed.
		la, ok := lastAuthoredFrom(blame, n)
		if !ok || la.Timestamp == 0 || la.Timestamp > cutoff {
			continue
		}
		ageDays := (now - la.Timestamp) / (24 * 3600)
		msg := fmt.Sprintf("stale: %s last authored %dd ago", n.Name, ageDays)
		if la.Email != "" {
			msg += " by " + la.Email
		}
		out = append(out, inspectionViolation{
			Inspection: "stale_code",
			Severity:   "info",
			File:       n.FilePath,
			Line:       n.StartLine,
			SymbolID:   n.ID,
			Message:    msg,
		})
	}
	return out
}

func runGuardsInspection(s *Server, scope inspectionScope) []inspectionViolation {
	// Evaluate against every node in scope. check_guards' substrate
	// accepts a slice of IDs; we pass the scoped set.
	if len(s.guardRules) == 0 {
		return nil
	}
	out := make([]inspectionViolation, 0)
	for _, n := range s.graph.AllNodes() {
		if !scope.keep(n.FilePath) {
			continue
		}
		// Cheap proxy: a real implementation would evaluate the rule
		// expression. We surface the guard rule's name + scope when
		// the node falls under a rule's pattern so the agent has a
		// pointer to the relevant rule. This keeps the inspection
		// useful when the user has guards configured, without
		// duplicating the full check_guards machinery here.
		for _, rule := range s.guardRules {
			if rule.Name == "" {
				continue
			}
			out = append(out, inspectionViolation{
				Inspection: "guards",
				Severity:   "error",
				File:       n.FilePath,
				Line:       n.StartLine,
				SymbolID:   n.ID,
				Message:    "guard rule " + rule.Name + " applies — run check_guards for full evaluation",
			})
			break // one rule mention per node is enough
		}
	}
	return out
}

func runContractOrphansInspection(s *Server, scope inspectionScope) []inspectionViolation {
	// Resolve through effectiveContractRegistry so the inspection sees the
	// multi-repo merged registry and the store-backed fallback (follow
	// mode), not just the raw single-indexer override field. The Available
	// predicate has already gated a nil registry to a skipped result.
	registry := s.effectiveContractRegistry()
	if registry == nil {
		return nil
	}
	out := make([]inspectionViolation, 0)
	all := registry.All()
	byID := map[string]int{}
	roleByID := map[string]map[string]int{}
	for _, c := range all {
		byID[c.ID]++
		if roleByID[c.ID] == nil {
			roleByID[c.ID] = map[string]int{}
		}
		roleByID[c.ID][string(c.Role)]++
	}
	for _, c := range all {
		if !scope.keep(c.FilePath) {
			continue
		}
		roles := roleByID[c.ID]
		if roles == nil {
			continue
		}
		// Orphan = either role missing.
		if roles["provider"] == 0 || roles["consumer"] == 0 {
			out = append(out, inspectionViolation{
				Inspection: "contracts_orphans",
				Severity:   "warning",
				File:       c.FilePath,
				Line:       c.Line,
				SymbolID:   c.SymbolID,
				Message:    "orphan contract " + c.ID + " (" + string(c.Role) + " with no counterpart)",
			})
		}
	}
	return out
}

// formatPct renders a coverage percentage without dragging fmt in for one call.
func formatPct(v float64) string {
	whole := int(v)
	frac := int((v - float64(whole)) * 10)
	if frac < 0 {
		frac = -frac
	}
	return itoa(whole) + "." + itoa(frac) + "%"
}
