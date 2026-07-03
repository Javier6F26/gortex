package graph

import (
	"sort"
	"strconv"
	"strings"
)

// Edge call-site multiplicity.
//
// The graph natively keys edges by (From, To, Kind, FilePath, Line), so an AST
// extractor that emits one edge per call site preserves in-file multiplicity
// on its own. A *synthesized* producer that can only mint one edge per
// (From, To) — e.g. the LSP references-add pass, which sees N reference sites
// for one declaration — would otherwise collapse those N sites to one. Rather
// than mint N near-identical edges, such a producer keeps one edge (its
// primary site in FilePath/Line) and records the additional sites in
// Meta["call_sites"]; find_usages expands them back into one row per site.
//
// Meta-only: no Edge struct field is added, so no wire-contract / GCX encoder
// churn. Edge.Meta round-trips through the store, so the sites survive a warm
// restart on the sqlite backend (via AddBatch / PersistEdge).

// AppendCallSite records an additional call/reference site on an edge whose
// primary site stays in FilePath/Line. Extra sites are stored in
// Meta["call_sites"] as sorted, deduped "<file>:<line>" strings; the primary
// site is never duplicated there.
func AppendCallSite(e *Edge, filePath string, line int) {
	if e == nil || filePath == "" || line <= 0 {
		return
	}
	if filePath == e.FilePath && line == e.Line {
		return // the primary site lives in FilePath/Line, not call_sites
	}
	site := filePath + ":" + strconv.Itoa(line)
	sites := CallSites(e)
	for _, s := range sites {
		if s == site {
			return
		}
	}
	sites = append(sites, site)
	sort.Strings(sites)
	if e.Meta == nil {
		e.Meta = map[string]any{}
	}
	e.Meta["call_sites"] = sites
}

// CallSites returns the extra "<file>:<line>" sites recorded on an edge,
// tolerating both the in-memory []string form and the []any form a JSON meta
// round-trip (disk backend) produces.
func CallSites(e *Edge) []string {
	if e == nil || e.Meta == nil {
		return nil
	}
	switch v := e.Meta["call_sites"].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// SplitCallSite splits a "<file>:<line>" call-site string into its file and
// 1-based line, returning ("", 0) when malformed. It splits on the last colon
// so a path that itself contains a colon is handled.
func SplitCallSite(site string) (string, int) {
	i := strings.LastIndexByte(site, ':')
	if i <= 0 || i == len(site)-1 {
		return "", 0
	}
	line, err := strconv.Atoi(site[i+1:])
	if err != nil || line <= 0 {
		return "", 0
	}
	return site[:i], line
}
