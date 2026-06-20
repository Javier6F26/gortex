package mcp

import "strings"

// analyzeKinds is the single source of truth for every `kind` the
// `analyze` dispatcher (handleAnalyze, tools_enhancements.go) accepts.
// It is the exact, sorted set of every `case` label in that switch —
// including the two that share one case (sast, hygiene) and every alias
// (review, domain, dbt_models, concepts, …). The AST-based anti-drift
// test in analyze_kinds_test.go asserts this slice equals the switch's
// case set exactly, so the two can never silently diverge.
//
// Keep it sorted: AnalyzeKinds returns a copy that callers may rely on
// being in stable order, and analyze_kinds_test.go asserts sortedness.
var analyzeKinds = []string{
	"annotation_users",
	"blame",
	"bottlenecks",
	"cgo_users",
	"channel_ops",
	"clusters",
	"components",
	"concepts",
	"config_readers",
	"connectivity_health",
	"constructors_missing_fields",
	"coverage",
	"coverage_gaps",
	"coverage_summary",
	"cross_repo",
	"cycles",
	"dbt_models",
	"dead_code",
	"def_use",
	"domain",
	"edge_audit",
	"env_var_users",
	"error_surface",
	"event_emitters",
	"external_calls",
	"field_writers",
	"fixes_history",
	"goroutine_spawns",
	"health_score",
	"hotspots",
	"hygiene",
	"images",
	"impact",
	"indirect_mutations",
	"k8s_resources",
	"kcore",
	"kustomize",
	"log_events",
	"louvain",
	"models",
	"named",
	"orphan_tables",
	"ownership",
	"pagerank",
	"pubsub",
	"race_writes",
	"ref_facts",
	"releases",
	"resolution_outcomes",
	"retrieval_log",
	"review",
	"role",
	"routes",
	"sast",
	"scc",
	"speculative",
	"sql_call_sites",
	"sql_rebuild",
	"stale_code",
	"stale_flags",
	"string_emitters",
	"suggest_boundaries",
	"synthesizers",
	"temporal_orphans",
	"temporal_verify",
	"tests_as_edges",
	"todos",
	"unclosed_channels",
	"unreferenced_tables",
	"unsafe_patterns",
	"wasm_users",
	"wcc",
	"would_create_cycle",
}

// AnalyzeKinds returns a defensive copy of the canonical analyze-kind
// set, in sorted order. Callers must not mutate the returned slice's
// backing array of the package-level source.
func AnalyzeKinds() []string {
	out := make([]string, len(analyzeKinds))
	copy(out, analyzeKinds)
	return out
}

// analyzeKindsCSV returns the canonical analyze kinds comma-joined, for
// interpolation into tool descriptions and the dispatcher's error
// strings so those lists always match the switch exactly.
func analyzeKindsCSV() string {
	return strings.Join(analyzeKinds, ", ")
}
