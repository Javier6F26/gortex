package mcp

import (
	"fmt"
	"sort"
	"strings"

	"github.com/zzet/gortex/internal/astquery"
)

// analyzeGroupedSummary is the compact description body for the `analyze`
// tool. The full per-kind reference used to be inlined here (thousands of
// bytes paid on every cold tools/list); it now lives once in the
// gortex://schema resource and in the tool's own `kind:"help"` response, so
// agents without resource support stay covered. The `kind` enum parameter
// still lists every valid kind by name.
const analyzeGroupedSummary = "Unified graph analysis — one tool, many kinds (pass exactly one `kind`). Families:\n" +
	"- structural: dead_code, hotspots, cycles, would_create_cycle, clusters, suggest_boundaries, connectivity_health, pagerank, kcore\n" +
	"- comments / churn: todos, stale_code, ownership, doc_staleness\n" +
	"- coverage / releases: coverage, coverage_gaps, coverage_summary, releases, blame\n" +
	"- security: sast, hygiene, review, named, unsafe_patterns\n" +
	"- framework: routes, models, components, dbt_models\n" +
	"- infra: k8s_resources, images, kustomize\n" +
	"- edge-driven: channel_ops, goroutine_spawns, field_writers, annotation_users, config_readers, event_emitters, error_surface, external_calls\n" +
	"- scoring: impact, bottlenecks, health_score\n" +
	"- multi-repo: cross_repo\n" +
	"Every kind takes the shared repo/project/workspace/scope filters (clamped to the session workspace; a scope_note flags kinds not repo-narrowed in v1). " +
	"For the one-line reference of EVERY kind, pass kind:\"help\" — or read the gortex://schema resource. The `kind` parameter enumerates all valid names."

// analyzeCatalogText renders the full per-kind reference (kind — one-line
// summary), name-sorted. Backs both the `analyze kind:"help"` response and
// the analyze section of the gortex://schema resource, so the catalog has a
// single source of truth (analyzeKindDescriptions).
func analyzeCatalogText() string {
	names := make([]string, 0, len(analyzeKindDescriptions))
	for k := range analyzeKindDescriptions {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	fmt.Fprintf(&b, "analyze — %d kinds (pass one as `kind`):\n", len(names))
	for _, k := range names {
		fmt.Fprintf(&b, "- %s: %s\n", k, analyzeKindDescriptions[k])
	}
	return b.String()
}

// analyzeHelpResult is the tool-side response for `analyze kind:"help"` —
// the full catalogue an agent gets without needing MCP resource support.
func analyzeHelpResult() string {
	return analyzeCatalogText() +
		"\nUsage: analyze kind:\"<name>\" [+ kind-specific args]. Same reference lives in the gortex://schema resource."
}

// searchASTDetectorFamilies groups the bundled detector names by their
// naming convention so the summary can report counts per family without
// enumerating all ~200 rules.
func searchASTDetectorFamilies() (bundled, sast, hygiene, review []string) {
	for _, d := range astquery.DescribeDetectors() {
		switch {
		case strings.HasPrefix(d.Name, "hygiene-"):
			hygiene = append(hygiene, d.Name)
		case strings.HasPrefix(d.Name, "review-"):
			review = append(review, d.Name)
		case strings.ContainsAny(d.Name, "-") && isSASTPrefixed(d.Name):
			sast = append(sast, d.Name)
		default:
			bundled = append(bundled, d.Name)
		}
	}
	sort.Strings(bundled)
	sort.Strings(sast)
	sort.Strings(hygiene)
	sort.Strings(review)
	return bundled, sast, hygiene, review
}

// isSASTPrefixed reports whether a detector name follows the per-language
// SAST naming (py-* / go-* / js-* / ts-* / java-* / ruby-* / php-* / rust-*).
func isSASTPrefixed(name string) bool {
	for _, p := range []string{"py-", "go-", "js-", "ts-", "java-", "ruby-", "php-", "rust-"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// buildSearchASTDescription assembles the compact `search_ast` description:
// how to run a pattern or a detector, the graph-aware filters, the raw
// S-expression syntax guide, and a per-family detector COUNT with the
// flagship bundled names. The full ~200-rule catalogue (which used to be
// inlined here, ~46KB on every full-preset tools/list) now lives in the
// tool's `detector:"help"` response and the gortex://schema resource.
func buildSearchASTDescription() string {
	bundled, sast, hygiene, review := searchASTDetectorFamilies()
	var b strings.Builder
	b.WriteString("Structural, graph-aware code search. ")
	b.WriteString("Run a tree-sitter pattern (`pattern: \"...\"`) or a bundled detector (`detector: \"<name>\"`) ")
	b.WriteString("across every indexed file in scope. Each result is enriched with the enclosing function's `symbol_id` ")
	b.WriteString("so you can chain straight into `find_usages`, `verify_change`, or `apply_code_action`.\n\n")
	b.WriteString("Graph-aware filters that ast-grep can't express: `path_prefix`, `repo`/`project`/`ref`, `min_fan_in_of_enclosing_func`. ")
	b.WriteString("Test files are excluded by default for detectors; opt back in via `exclude_tests: false`.\n\n")
	fmt.Fprintf(&b, "**Detectors:** %d bundled + %d SAST + %d hygiene + %d review rules. ",
		len(bundled), len(sast), len(hygiene), len(review))
	if len(bundled) > 0 {
		b.WriteString("Bundled: `" + strings.Join(bundled, "`, `") + "`. ")
	}
	b.WriteString("For the full catalogue (every rule with severity + languages) pass `detector: \"help\"`, or read the gortex://schema resource.\n\n")
	b.WriteString("**Raw pattern syntax:** standard tree-sitter S-expression queries. Anchor the match span with `@match`. ")
	b.WriteString("Predicates: `(#eq? @x \"literal\")`, `(#match? @x \"regex\")`. ")
	b.WriteString("Example: `((call_expression function: (identifier) @fn) @match (#eq? @fn \"panic\"))` finds every direct panic() call.")
	return b.String()
}

// searchASTDetectorCatalogText renders the full detector catalogue (every
// rule with severity + supported languages), name-sorted. Backs both the
// `search_ast detector:"help"` response and the schema resource.
func searchASTDetectorCatalogText() string {
	ds := astquery.DescribeDetectors()
	sort.Slice(ds, func(i, j int) bool { return ds[i].Name < ds[j].Name })
	var b strings.Builder
	fmt.Fprintf(&b, "search_ast detectors — %d rules:\n", len(ds))
	for _, d := range ds {
		fmt.Fprintf(&b, "- %s (%s) — %s [%s]\n",
			d.Name, d.Severity, d.Description, strings.Join(d.Languages, ", "))
	}
	return b.String()
}

// searchASTHelpResult is the tool-side response for
// `search_ast detector:"help"`.
func searchASTHelpResult() string {
	return searchASTDetectorCatalogText() +
		"\nUsage: search_ast detector:\"<name>\" or pattern:\"<s-expr>\" language:\"<lang>\". Same catalogue lives in the gortex://schema resource."
}
