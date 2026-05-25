//go:build kuzu

// Command kuzu-stubs indexes a repo through kuzu, then classifies the
// node set into "real" rows (caller went through AddNode with a
// populated kind/name) vs "stub" rows (auto-materialised by COPY's FK
// guard with everything blank but the ID). For each population, prints
// an ID-prefix histogram so we can confirm what's actually inflating
// the node count.
//
// The interesting question this answers: are the stubs ONLY for
// expected unresolved/external IDs the resolver couldn't bind, or are
// any of them "real-looking" pkg/file.go::Foo IDs that would point at
// a parser→indexer bug (edge emitted for a symbol that never got an
// AddNode call)?
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/graph/store_kuzu"
	"github.com/zzet/gortex/internal/indexer"
	"github.com/zzet/gortex/internal/parser"
	"github.com/zzet/gortex/internal/parser/languages"
)

func main() {
	root := flag.String("root", "", "repo root (required)")
	workers := flag.Int("workers", runtime.NumCPU(), "indexer parallelism")
	sampleLimit := flag.Int("samples", 12, "max sample IDs to dump per category")
	flag.Parse()
	if *root == "" {
		fmt.Fprintln(os.Stderr, "usage: kuzu-stubs -root <path>")
		os.Exit(1)
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		panic(err)
	}

	// Index through kuzu.
	dir, err := os.MkdirTemp("", "kuzu-stubs-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	store, err := store_kuzu.Open(filepath.Join(dir, "store.kuzu"))
	if err != nil {
		panic(err)
	}

	fmt.Fprintln(os.Stderr, "indexing through kuzu...")
	reg := parser.NewRegistry()
	languages.RegisterAll(reg)
	cfg := config.Config{}
	cfg.Index.Workers = *workers
	idx := indexer.New(store, reg, cfg.Index, zap.NewNop())
	if _, err := idx.IndexCtx(context.Background(), abs); err != nil {
		panic(err)
	}

	nodes := store.AllNodes()
	edges := store.AllEdges()

	// Classify.
	stubByPrefix := map[string]*bucket{}
	realByPrefix := map[string]*bucket{}

	stubCount, realCount := 0, 0
	for _, n := range nodes {
		isStub := n.Kind == "" && n.Name == "" && n.FilePath == ""
		prefix := classifyIDPrefix(n.ID)
		var m map[string]*bucket
		if isStub {
			stubCount++
			m = stubByPrefix
		} else {
			realCount++
			m = realByPrefix
		}
		b, ok := m[prefix]
		if !ok {
			b = &bucket{}
			m[prefix] = b
		}
		b.count++
		if len(b.ids) < *sampleLimit {
			b.ids = append(b.ids, n.ID)
		}
	}

	// Count edge fan-in to each stub bucket — confirms stubs are real
	// targets of edges, not just orphan rows the indexer dropped in.
	stubIDs := make(map[string]struct{}, stubCount)
	for _, n := range nodes {
		if n.Kind == "" && n.Name == "" && n.FilePath == "" {
			stubIDs[n.ID] = struct{}{}
		}
	}
	stubFanInByPrefix := map[string]int{}
	totalEdges := 0
	for _, e := range edges {
		totalEdges++
		if _, ok := stubIDs[e.To]; ok {
			stubFanInByPrefix[classifyIDPrefix(e.To)]++
		}
	}

	// Real-looking stubs are the bug indicator: stubs whose ID doesn't
	// match any known "synthetic" prefix.
	suspectStubs := []string{}
	for _, n := range nodes {
		if n.Kind != "" || n.Name != "" || n.FilePath != "" {
			continue
		}
		if !isSyntheticID(n.ID) {
			suspectStubs = append(suspectStubs, n.ID)
		}
	}
	sort.Strings(suspectStubs)

	fmt.Printf("kuzu store: %d total nodes, %d edges\n", len(nodes), totalEdges)
	fmt.Printf("  real (kind/name/file populated): %d\n", realCount)
	fmt.Printf("  stub (all populated fields empty): %d\n", stubCount)
	fmt.Printf("  suspect stubs (real-looking ID with no fields): %d\n", len(suspectStubs))
	fmt.Println()

	fmt.Println("=== stub ID-prefix histogram (kind=empty, name=empty, file=empty) ===")
	dumpBuckets(stubByPrefix, stubFanInByPrefix, *sampleLimit)

	fmt.Println()
	fmt.Println("=== real-node ID-prefix histogram (for comparison) ===")
	dumpBuckets(realByPrefix, nil, *sampleLimit)

	if len(suspectStubs) > 0 {
		// Build a To→edges index so we can describe what edge kinds
		// reference each suspect — that tells us WHY a "real-looking"
		// ID became a stub (mis-resolved method receiver? mis-emitted
		// import target? something else).
		suspectSet := map[string]struct{}{}
		for _, id := range suspectStubs {
			suspectSet[id] = struct{}{}
		}
		inEdges := map[string][]*graph.Edge{}
		for _, e := range edges {
			if _, ok := suspectSet[e.To]; ok {
				inEdges[e.To] = append(inEdges[e.To], e)
			}
		}
		// Classify suspects by ID family + edge-kind signature.
		type sig struct{ family, kindSig string }
		hist := map[sig]int{}
		samples := map[sig][]string{}
		for _, id := range suspectStubs {
			fam := suspectFamily(id)
			kinds := map[graph.EdgeKind]int{}
			for _, e := range inEdges[id] {
				kinds[e.Kind]++
			}
			kindSig := edgeKindSig(kinds)
			s := sig{fam, kindSig}
			hist[s]++
			if len(samples[s]) < 6 {
				samples[s] = append(samples[s], id)
			}
		}
		type row struct {
			s sig
			n int
		}
		rows := make([]row, 0, len(hist))
		for s, n := range hist {
			rows = append(rows, row{s, n})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].n > rows[j].n })
		fmt.Println()
		fmt.Println("=== SUSPECT STUBS — by family / edge-kind signature ===")
		for _, r := range rows {
			fmt.Printf("  family=%-30s kinds=%-30s count=%d\n", r.s.family, r.s.kindSig, r.n)
			for _, id := range samples[r.s] {
				if len(id) > 100 {
					id = id[:97] + "..."
				}
				fmt.Printf("    %q\n", id)
			}
		}
	} else {
		fmt.Println()
		fmt.Println("OK: every stub has a synthetic ID prefix (unresolved/external/etc) — no parser→indexer leak.")
	}
}

// classifyIDPrefix buckets an ID by its leading marker. Real symbol
// IDs (pkg/file.go::Foo) get classified as "real:<extension>" so we
// can spot any "real-looking" IDs leaking into the stub population.
// `#local:*@line` and `#param:*`/`#closure@*` suffixes are also broken
// out because they sit on top of a real symbol ID — they're per-frame
// references the parser emits.
func classifyIDPrefix(id string) string {
	switch {
	case strings.HasPrefix(id, "unresolved::pyrel::"):
		return "unresolved::pyrel::*"
	case strings.HasPrefix(id, "unresolved::"):
		return "unresolved::*"
	case strings.HasPrefix(id, "external::"):
		return "external::*"
	case strings.HasPrefix(id, "module::pypi:"):
		return "module::pypi:*"
	case strings.HasPrefix(id, "module::python:stdlib"):
		return "module::python:stdlib::*"
	case strings.HasPrefix(id, "module::"):
		return "module::*"
	case strings.HasPrefix(id, "dep::"):
		return "dep::*"
	case strings.HasPrefix(id, "annotation::"):
		return "annotation::*"
	case strings.HasPrefix(id, "contract::"):
		return "contract::*"
	case strings.HasPrefix(id, "test::"):
		return "test::*"
	case strings.HasPrefix(id, "stdlib::"):
		return "stdlib::*"
	}
	if i := strings.Index(id, "::"); i > 0 {
		// pkg/file.go::Foo shape — symbol ID. Further split by the
		// per-frame suffix the parser appends for locals/params/closures.
		head := id[:i]
		tail := id[i+2:]
		var subKind string
		switch {
		case strings.Contains(tail, "#local:"):
			subKind = "#local:*"
		case strings.Contains(tail, "#param:"):
			subKind = "#param:*"
		case strings.Contains(tail, "#closure"):
			subKind = "#closure"
		case strings.Contains(tail, "#"):
			subKind = "#other"
		default:
			subKind = "(no-suffix)"
		}
		ext := filepath.Ext(head)
		if ext == "" {
			ext = "(no-ext)"
		}
		return "real:" + ext + " " + subKind
	}
	// Bare file-path ID (no `::`) — likely a KindFile node.
	if ext := filepath.Ext(id); ext != "" {
		return "file:" + ext
	}
	return "bare-id"
}

func isSyntheticID(id string) bool {
	prefixes := []string{
		"unresolved::", "external::", "module::", "dep::",
		"annotation::", "contract::", "test::", "exception::",
		"taint::", "queue::", "channel::", "secret::",
		"thread::", "goroutine::", "pyrel::", "stdlib::",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	// `<ownerID>#local:<name>@<line>`, `#param:<name>`, `#closure@<line>`
	// are intentionally edge-only references — see comment on
	// emitGoDataflow in internal/parser/languages/go_dataflow.go. These
	// are not bugs; the parser elects not to materialise per-binding
	// nodes to keep symbol search clean.
	if strings.Contains(id, "#local:") ||
		strings.Contains(id, "#param:") ||
		strings.Contains(id, "#closure") ||
		strings.Contains(id, "#field:") ||
		strings.Contains(id, "#method_recv") {
		return true
	}
	return false
}

func dumpBuckets(m map[string]*bucket, fanIn map[string]int, sampleLimit int) {
	type row struct {
		prefix string
		b      *bucket
	}
	rows := make([]row, 0, len(m))
	for p, b := range m {
		rows = append(rows, row{p, b})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].b.count > rows[j].b.count })
	for _, r := range rows {
		fi := ""
		if fanIn != nil {
			fi = fmt.Sprintf(" (fan-in: %d edges)", fanIn[r.prefix])
		}
		fmt.Printf("  %-30s -> %d%s\n", r.prefix, r.b.count, fi)
		for _, id := range r.b.ids {
			if len(id) > 90 {
				id = id[:87] + "..."
			}
			fmt.Printf("    %q\n", id)
		}
	}
}

type bucket struct {
	count int
	ids   []string
}

// suspectFamily buckets a suspect-stub ID by a coarse shape so we can
// see whether the misattribution affects only one parser/pass or
// spans several.
func suspectFamily(id string) string {
	switch {
	case strings.HasPrefix(id, "builtin::py::"):
		return "builtin::py"
	case strings.HasPrefix(id, "builtin::ts::"):
		return "builtin::ts"
	case strings.HasPrefix(id, "image::stage::"):
		return "image::stage"
	}
	if i := strings.Index(id, "::"); i > 0 {
		head := id[:i]
		ext := filepath.Ext(head)
		if ext == "" {
			ext = "(no-ext)"
		}
		return "real-symbol:" + ext
	}
	return "other"
}

func edgeKindSig(kinds map[graph.EdgeKind]int) string {
	if len(kinds) == 0 {
		return "(no-inbound-edges)"
	}
	names := make([]string, 0, len(kinds))
	for k := range kinds {
		names = append(names, string(k))
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
