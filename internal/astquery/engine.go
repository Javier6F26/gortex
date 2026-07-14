// Package astquery is the structural code-search engine behind the
// `search_ast` MCP tool. It runs tree-sitter pattern queries (raw
// S-expressions or one of the bundled named detectors) against
// already-indexed files and returns each match enriched with the
// enclosing symbol from the graph, the captured nodes, and the
// detector metadata.
//
// The package is deliberately graph-aware: results carry a
// `SymbolID` field so a caller can chain straight into
// `find_usages`, `verify_change`, or `apply_code_action` without a
// second graph walk. The pre-filter side of the same coin is on the
// caller — the MCP wrapper decides which (file, language) targets to
// hand the engine, scoping by repo / project / community / churn /
// fan-in / path-prefix before the engine spends a single tree-sitter
// parse. That two-layer split keeps astquery independently testable
// (no graph dependency) while still letting the wrapper exploit
// graph predicates ast-grep can't express.
package astquery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/zzet/gortex/internal/parser"
	sitter "github.com/zzet/gortex/internal/parser/tsitter"
)

// Target points the engine at one indexed file. Both the on-disk path
// (for parsing) and the repo-prefixed graph path (for result
// enrichment and SymbolLookup) are needed because the graph speaks
// repo-relative IDs and the filesystem speaks absolute paths.
type Target struct {
	AbsPath   string
	GraphPath string
	Language  string
}

// SymbolLookup resolves the enclosing function/method/closure at a
// 1-based line in a graph-relative file. The MCP layer wires this
// against `*graph.Graph`; tests can pass a stub. Returning empty
// strings is fine — matches simply ship without symbol enrichment.
type SymbolLookup func(graphPath string, line int) (symbolID, symbolName string)

// LanguageResolver maps a language name (as stored on KindFile nodes —
// "go", "python", "typescript", …) to its tree-sitter binding. The
// engine ships a default resolver covering the languages used by the
// bundled detectors; consumers can pass an extended resolver to
// support raw-pattern queries against any language Gortex indexes.
type LanguageResolver func(name string) *sitter.Language

// Options are the engine's run knobs. Pattern XOR Detector must be
// set; the engine does not invent a detector when both are blank.
type Options struct {
	// Pattern is a raw tree-sitter S-expression query. Used when
	// Detector is empty. The pattern's language is inferred from
	// each Target's Language field — one compiled query per
	// distinct (pattern, language) pair, cached for the run.
	Pattern string

	// Detector is the name of a bundled detector. When set, Pattern
	// and Language are ignored; the engine picks the per-language
	// pattern from the detector definition.
	Detector string

	// Language pre-filters Targets when Pattern is set: only
	// targets whose Language matches this string are processed.
	// Empty = no language filter.
	Language string

	// Targets is the file set to scan. The MCP wrapper builds this
	// from the graph after applying scope predicates (repo /
	// project / community / fan-in / churn / path-prefix); the
	// engine itself does no graph walk.
	Targets []Target

	// SymbolLookup is consulted after a match to enrich the row
	// with the enclosing function. Optional; nil leaves SymbolID
	// blank.
	SymbolLookup SymbolLookup

	// Resolver maps language names to tree-sitter bindings.
	// Required for both raw-pattern and detector runs (a detector's
	// per-language patterns still need the binding to compile).
	// Pass DefaultLanguageResolver for the bundled set.
	Resolver LanguageResolver

	// Limit caps the total returned matches. 0 means use Default.
	Limit int

	// MaxMatchText truncates each row's `MatchText` to this many
	// bytes (utf-8 safe — we cut on rune boundaries). 0 → 200.
	MaxMatchText int

	// ExcludeTests drops matches whose target file path looks like
	// a test (per IsTestPath). Detectors default this to true; raw
	// patterns default to false because the user typed the query.
	ExcludeTests bool

	// Concurrency caps the worker pool. 0 → runtime.GOMAXPROCS(0).
	Concurrency int

	// Source, when set, supplies a target's bytes instead of reading
	// t.AbsPath from disk — the FileSource seam that lets a diskless
	// follower parse from store blobs (code-source-blobs D7). A non-nil
	// error skips the target (recorded like any per-file read error).
	// Nil = read from disk (the default behaviour).
	Source func(t Target) ([]byte, error)
}

// Match is a single hit. The fields are deliberately flat so the MCP
// layer can hand them to the standard wire-format encoders without
// translation.
type Match struct {
	File       string            `json:"file"`
	Line       int               `json:"line"`
	EndLine    int               `json:"end_line"`
	Column     int               `json:"column"`
	EndCol     int               `json:"end_col"`
	SymbolID   string            `json:"symbol_id,omitempty"`
	SymbolName string            `json:"symbol_name,omitempty"`
	Detector   string            `json:"detector,omitempty"`
	Severity   string            `json:"severity,omitempty"`
	Language   string            `json:"language"`
	Text       string            `json:"text"`
	Captures   map[string]string `json:"captures,omitempty"`
}

// Result wraps the match list with summary counts. `Truncated` is set
// when Limit cut the response short — useful for the MCP layer to
// decide whether to surface a "narrow your filter" hint.
type Result struct {
	Matches    []Match `json:"matches"`
	Total      int     `json:"total"`
	Truncated  bool    `json:"truncated,omitempty"`
	FilesWalked int    `json:"files_walked"`
	Errors     []string `json:"errors,omitempty"`
}

const (
	defaultLimit         = 50
	defaultMaxMatchText  = 200
	defaultMaxFileSize   = 4 * 1024 * 1024
)

var errNoQuery = errors.New("astquery: pattern or detector is required")

// Run executes the configured query across all Targets and returns
// the assembled Result. The function is safe to call concurrently.
//
// Compilation strategy: each (pattern, language) pair is compiled
// once for the run via per-language `*parser.PreparedQuery` instances
// kept in a local cache. After Run returns the cache is closed so we
// don't leak C resources across runs.
func Run(ctx context.Context, opts Options) (Result, error) {
	if opts.Pattern == "" && opts.Detector == "" {
		return Result{}, errNoQuery
	}
	if opts.Resolver == nil {
		opts.Resolver = DefaultLanguageResolver
	}
	if opts.Limit <= 0 {
		opts.Limit = defaultLimit
	}
	if opts.MaxMatchText <= 0 {
		opts.MaxMatchText = defaultMaxMatchText
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = runtime.GOMAXPROCS(0)
	}

	plan, err := buildPlan(opts)
	if err != nil {
		return Result{}, err
	}
	defer plan.close()

	if len(plan.targets) == 0 {
		return Result{}, nil
	}

	jobs := make(chan Target, len(plan.targets))
	results := make(chan []Match, opts.Concurrency)
	errCh := make(chan string, opts.Concurrency)

	var wg sync.WaitGroup
	for i := 0; i < opts.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				if ctx.Err() != nil {
					return
				}
				m, e := plan.runTarget(ctx, t, opts)
				if e != nil {
					select {
					case errCh <- fmt.Sprintf("%s: %v", t.GraphPath, e):
					default:
					}
				}
				if len(m) > 0 {
					results <- m
				}
			}
		}()
	}

	go func() {
		for _, t := range plan.targets {
			jobs <- t
		}
		close(jobs)
		wg.Wait()
		close(results)
		close(errCh)
	}()

	out := Result{FilesWalked: len(plan.targets)}
	for batch := range results {
		out.Matches = append(out.Matches, batch...)
	}
	for e := range errCh {
		out.Errors = append(out.Errors, e)
	}

	// Stable order: by file then line. Important for golden tests
	// and for agent UX (consistent listings across reruns).
	sort.Slice(out.Matches, func(i, j int) bool {
		if out.Matches[i].File != out.Matches[j].File {
			return out.Matches[i].File < out.Matches[j].File
		}
		return out.Matches[i].Line < out.Matches[j].Line
	})

	out.Total = len(out.Matches)
	if out.Total > opts.Limit {
		out.Matches = out.Matches[:opts.Limit]
		out.Truncated = true
	}
	return out, ctx.Err()
}

// plan is the per-run state: one compiled query per language, the
// target list filtered by Language and ExcludeTests, and the detector
// definition (if any).
type plan struct {
	queries  map[string]*parser.PreparedQuery
	detector *Detector
	targets  []Target
	pattern  string
}

func buildPlan(opts Options) (*plan, error) {
	p := &plan{queries: make(map[string]*parser.PreparedQuery)}

	// Detector mode: ignore Pattern + Language; the detector's
	// per-language map drives both query compilation and target
	// filtering.
	if opts.Detector != "" {
		d, ok := lookupDetector(opts.Detector)
		if !ok {
			return nil, fmt.Errorf("astquery: unknown detector %q (call ListDetectors to enumerate)", opts.Detector)
		}
		p.detector = d
		for _, t := range opts.Targets {
			if _, has := d.Languages[t.Language]; !has {
				continue
			}
			if (opts.ExcludeTests || d.ExcludeTests) && IsTestPath(t.AbsPath) {
				continue
			}
			p.targets = append(p.targets, t)
		}
		// Compile one query per language used by the detector,
		// but only languages that actually have targets — saves
		// CGO compile cost when the index is monolingual.
		used := make(map[string]bool)
		for _, t := range p.targets {
			used[t.Language] = true
		}
		for lang := range used {
			pat, ok := d.Languages[lang]
			if !ok {
				continue
			}
			tsLang := opts.Resolver(lang)
			if tsLang == nil {
				continue
			}
			q, err := parser.NewPreparedQuery(pat, tsLang)
			if err != nil {
				p.close()
				return nil, fmt.Errorf("astquery: detector %q has invalid pattern for language %q: %w", d.Name, lang, err)
			}
			p.queries[lang] = q
		}
		return p, nil
	}

	// Raw-pattern mode: one query, one language.
	p.pattern = opts.Pattern
	for _, t := range opts.Targets {
		if opts.Language != "" && t.Language != opts.Language {
			continue
		}
		if opts.ExcludeTests && IsTestPath(t.AbsPath) {
			continue
		}
		p.targets = append(p.targets, t)
	}
	used := make(map[string]bool)
	for _, t := range p.targets {
		used[t.Language] = true
	}
	for lang := range used {
		tsLang := opts.Resolver(lang)
		if tsLang == nil {
			continue
		}
		q, err := parser.NewPreparedQuery(opts.Pattern, tsLang)
		if err != nil {
			p.close()
			return nil, fmt.Errorf("astquery: pattern compile (lang=%s): %w", lang, err)
		}
		p.queries[lang] = q
	}
	if len(p.queries) == 0 {
		return p, nil
	}
	return p, nil
}

func (p *plan) close() {
	for _, q := range p.queries {
		q.Close()
	}
	p.queries = nil
}

func (p *plan) runTarget(ctx context.Context, t Target, opts Options) ([]Match, error) {
	var (
		src []byte
		err error
	)
	if opts.Source != nil {
		src, err = opts.Source(t)
	} else {
		src, err = readBoundedFile(t.AbsPath, defaultMaxFileSize)
	}
	if err != nil {
		return nil, err
	}
	return p.runBytes(ctx, t, opts, src)
}

func (p *plan) runBytes(_ context.Context, t Target, opts Options, src []byte) ([]Match, error) {
	q := p.queries[t.Language]
	if q == nil {
		return nil, nil
	}

	tsLang := opts.Resolver(t.Language)
	if tsLang == nil {
		return nil, fmt.Errorf("no tree-sitter binding for language %q", t.Language)
	}
	tree, err := parser.ParseFile(src, tsLang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	hits := parser.RunPrepared(q, tree.RootNode(), src)
	if len(hits) == 0 {
		return nil, nil
	}

	out := make([]Match, 0, len(hits))
	for _, h := range hits {
		// Prefer a "@match" capture as the row's anchor; fall back
		// to the largest captured node when the user didn't tag
		// one. This matches ast-grep's convention without making
		// every detector spell @match explicitly.
		anchor := pickAnchor(h.Captures)
		if anchor == nil {
			continue
		}
		text := truncateRune(anchor.Text, opts.MaxMatchText)
		caps := make(map[string]string, len(h.Captures))
		for name, cn := range h.Captures {
			if name == "match" {
				continue
			}
			caps[name] = truncateRune(cn.Text, opts.MaxMatchText)
		}
		m := Match{
			File:     t.GraphPath,
			Line:     anchor.StartLine + 1,
			EndLine:  anchor.EndLine + 1,
			Column:   anchor.StartCol,
			EndCol:   anchor.EndCol,
			Language: t.Language,
			Text:     text,
			Captures: caps,
		}
		if p.detector != nil {
			m.Detector = p.detector.Name
			m.Severity = p.detector.Severity
			if p.detector.PostFilter != nil && !p.detector.PostFilter(h, src) {
				continue
			}
		}
		if opts.SymbolLookup != nil {
			m.SymbolID, m.SymbolName = opts.SymbolLookup(t.GraphPath, m.Line)
		}
		out = append(out, m)
	}
	return out, nil
}

// pickAnchor selects the most useful capture as the result's anchor.
// Priority: explicit `@match` capture > longest captured span. The
// fallback is important for detectors that only define semantic
// captures and never an explicit `@match`.
func pickAnchor(caps map[string]*parser.CapturedNode) *parser.CapturedNode {
	if m, ok := caps["match"]; ok {
		return m
	}
	var best *parser.CapturedNode
	bestLen := -1
	for _, c := range caps {
		l := len(c.Text)
		if l > bestLen {
			best = c
			bestLen = l
		}
	}
	return best
}

func readBoundedFile(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > maxBytes {
		return nil, fmt.Errorf("file too large (%d bytes; cap %d)", st.Size(), maxBytes)
	}
	buf := make([]byte, st.Size())
	if _, err := f.Read(buf); err != nil && st.Size() != 0 {
		return nil, err
	}
	return buf, nil
}

// truncateRune cuts s to the first n bytes that fall on a UTF-8
// boundary. Appends "…" when truncation actually fired so consumers
// don't mistake a snipped result for a complete one.
func truncateRune(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Walk back to a rune start.
	for n > 0 && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n] + "…"
}

// RunOnSource executes one query against in-memory source bytes
// without touching the filesystem. Useful for unit tests, for the
// "lint this buffer before commit" UX, and for any caller that
// already has the bytes in hand. The function builds a Plan with a
// single synthetic Target whose GraphPath equals filePath; pass an
// empty filePath when the caller doesn't care.
//
// Limit / MaxMatchText / ExcludeTests are honored. SymbolLookup is
// honored too so a caller threading a graph in can still get
// enclosing-symbol enrichment for buffer-side lints.
func RunOnSource(ctx context.Context, opts Options, filePath, language string, src []byte) (Result, error) {
	if opts.Pattern == "" && opts.Detector == "" {
		return Result{}, errNoQuery
	}
	if opts.Resolver == nil {
		opts.Resolver = DefaultLanguageResolver
	}
	if opts.Limit <= 0 {
		opts.Limit = defaultLimit
	}
	if opts.MaxMatchText <= 0 {
		opts.MaxMatchText = defaultMaxMatchText
	}
	target := Target{
		AbsPath:   filePath,
		GraphPath: filePath,
		Language:  language,
	}
	opts.Targets = []Target{target}
	plan, err := buildPlan(opts)
	if err != nil {
		return Result{}, err
	}
	defer plan.close()

	matches, runErr := plan.runBytes(ctx, target, opts, src)
	out := Result{FilesWalked: 1}
	if runErr != nil {
		out.Errors = []string{fmt.Sprintf("%s: %v", filePath, runErr)}
	}
	out.Matches = matches
	out.Total = len(matches)
	if out.Total > opts.Limit {
		out.Matches = out.Matches[:opts.Limit]
		out.Truncated = true
	}
	return out, nil
}

// IsTestPath returns true when the file path looks like a test under
// any of Gortex's recognised conventions. The list is deliberately
// conservative; false negatives (a test we don't recognise) ship
// matches that the agent can drop, which is much better than false
// positives (skipping production code that happens to have "test" in
// the name).
func IsTestPath(absPath string) bool {
	base := filepath.Base(absPath)
	dir := filepath.ToSlash(filepath.Dir(absPath))
	switch {
	case strings.HasSuffix(base, "_test.go"):
		return true
	case strings.HasSuffix(base, ".test.ts"), strings.HasSuffix(base, ".test.tsx"),
		strings.HasSuffix(base, ".test.js"), strings.HasSuffix(base, ".test.jsx"),
		strings.HasSuffix(base, ".spec.ts"), strings.HasSuffix(base, ".spec.tsx"),
		strings.HasSuffix(base, ".spec.js"), strings.HasSuffix(base, ".spec.jsx"):
		return true
	case strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"):
		return true
	case strings.HasSuffix(base, "_test.py"):
		return true
	case strings.HasSuffix(base, "_test.rb"), strings.HasSuffix(base, "_spec.rb"):
		return true
	case strings.HasSuffix(base, "Test.java"), strings.HasSuffix(base, "Tests.java"),
		strings.HasSuffix(base, "IT.java"):
		return true
	case strings.Contains(dir, "/__tests__/") || strings.HasSuffix(dir, "/__tests__"):
		return true
	case strings.Contains(dir, "/test/") || strings.Contains(dir, "/tests/") || strings.Contains(dir, "/spec/"):
		return true
	}
	return false
}

// Compile-time checks: the runQuery hot path takes a *parser.PreparedQuery
// pointer; ensure we never lose the reference and force-close it under
// us. The plan owns every query and closes them in plan.close().
var _ = parser.RunPrepared
