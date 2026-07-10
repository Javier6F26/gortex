package rerank

import (
	"path"
	"regexp"
	"strings"

	"github.com/zzet/gortex/internal/excludes"
)

// PathPenaltySignal applies a multiplicative penalty to candidates
// whose file path falls into one of six "supporting cast" buckets —
// test files, compatibility shims, examples, type declarations,
// re-export barrels, and generated files that shadow a real
// implementation. The intuition: when an agent asks for the
// canonical definition of `validateToken`, the top hit should be the
// real implementation in `auth/token.go`, not the assertion in
// `auth/token_test.go` or the re-export in `index.ts`.
//
// Signals contribute in [0, 1] additively to a candidate's score, so
// "penalty" here is encoded as a smaller positive contribution: an
// uncategorised file gets 1.0, a test file gets 0.3, and so on. The
// pipeline weight scales the spread between buckets; with weight 0.4,
// a test-file penalty costs roughly 0.28 score points relative to a
// neutral file, which is enough to demote on ties but not enough to
// hide a strong BM25 + fan-in match.
//
// Tiers (multiplier returned):
//
//   - Test files       → 0.3 (the heaviest penalty: assertions and
//     fixtures should never outrank real code)
//   - Compatibility    → 0.5 (legacy / polyfill / shim — usually a
//     workaround, not the live implementation)
//   - Examples         → 0.5 (demo code; useful but never the truth)
//   - Type declarations → 0.7 (`.d.ts`, `.pyi`, `.h` headers — the
//     interface, not the implementation)
//   - Re-export barrels → 0.7 (`index.ts`, `__init__.py`, `mod.rs`,
//     `lib.rs` — a forwarding hop, not the source)
//   - Generated files  → 0.4 (`foo.pb.go`, `mock_x.go`, `x_pb2.py` —
//     ONLY when a real same-named hand-written peer exists in the
//     graph; a generated file that is the sole definition is left at
//     1.0 so it isn't demoted into oblivion)
//   - Anything else    → 1.0 (no penalty)
//
// When a file matches multiple rubrics the smaller (more aggressive)
// multiplier wins so penalties don't compound silently — a
// `tests/examples/foo.go` reads as a test, not as 0.3 * 0.5.
//
// Path classifications are cached per-Rerank call so the rubric runs
// once per unique file path rather than once per candidate.
type PathPenaltySignal struct{}

// Name returns the canonical signal name registered in DefaultWeights.
func (PathPenaltySignal) Name() string { return SignalPathPenalty }

// Contribute returns the per-candidate path-penalty multiplier in
// [0.3, 1.0]. Returns 1.0 for candidates with no file path so a
// missing path doesn't accidentally crush a candidate.
func (PathPenaltySignal) Contribute(_ string, c *Candidate, ctx *Context) float64 {
	if c == nil || c.Node == nil {
		return 1.0
	}
	fp := c.Node.FilePath
	if fp == "" {
		return 1.0
	}
	if ctx != nil && ctx.pathPenaltyCache != nil {
		if cached, ok := ctx.pathPenaltyCache[fp]; ok {
			return cached
		}
	}
	pen := classifyPathPenalty(fp)
	// Generated-file gate. A generated file (foo.pb.go, mock_x.go,
	// x_pb2.py, …) that shadows a real hand-written same-named peer
	// is supporting cast: the agent wants the implementation, not the
	// stub. Applied ONLY in the Uncatched branch so a generated file
	// that ALSO matches a heavier bucket (a generated test fixture,
	// say) keeps that heavier penalty — preserving "most aggressive
	// wins" — and ONLY when a non-generated peer actually exists in
	// the graph, so a generated file that is the sole definition
	// stays un-penalised. The peer lookup needs the graph, which is
	// why this lives in Contribute and not the pure classifier.
	if pen == PathPenaltyUncatched && ctx != nil && ctx.Graph != nil && excludes.IsGenerated(fp) {
		if generatedPeerExists(ctx, fp) {
			pen = PathPenaltyGenerated
		}
	}
	if ctx != nil && ctx.pathPenaltyCache != nil {
		ctx.pathPenaltyCache[fp] = pen
	}
	return pen
}

// generatedPeerExists reports whether a non-generated, hand-written
// file sharing the generated file's base name is present in the
// graph. The candidate peer paths come from excludes.GeneratedPeerPaths
// (foo.pb.go → foo.go, mock_user.go → user.go, …). A wrong-stem
// derivation yields a false "no peer", which is the safe outcome: no
// penalty rather than a spurious one.
func generatedPeerExists(ctx *Context, fp string) bool {
	for _, peer := range excludes.GeneratedPeerPaths(fp) {
		if len(ctx.Graph.GetFileNodes(peer)) > 0 {
			return true
		}
	}
	return false
}

// Penalty multiplier constants — exported so config / debug surfaces
// can refer to them without re-deriving the rubric.
const (
	PathPenaltyTest      = 0.3
	PathPenaltyGenerated = 0.4
	PathPenaltyCompat    = 0.5
	PathPenaltyExamples  = 0.5
	PathPenaltyTypeDecl  = 0.7
	PathPenaltyReexport  = 0.7
	PathPenaltyUncatched = 1.0
)

// Pre-compiled patterns. Built at package init so the rubric stays
// allocation-free on the hot path.
var (
	// Test paths across 16 language ecosystems. Matches any path
	// segment that looks like a test file across the conventions
	// listed below. Also any directory literally called test / tests
	// / __tests__ / spec / specs / e2e / fixtures / testdata covers
	// every language uniformly.
	//
	// File-suffix conventions:
	//   - Go        _test.go
	//   - Python    test_*.py, *_test.py
	//   - Ruby      *_test.rb, *_spec.rb
	//   - JS / TS   *.test.{js,jsx,ts,tsx,mjs,cjs}, *.spec.{...}
	//   - Swift     *Tests.swift, *Test.swift
	//   - Java      *Test.java
	//   - Kotlin    *Test.kt
	//   - Scala     *Test.scala
	//   - C#        *Test.cs
	//   - PHP       *Test.php, *_test.php
	//   - Elixir    *_test.exs
	//   - Rust      *_test.rs (the tests/ tree falls under the dir
	//               pattern; this catches inline test modules)
	//   - Dart      *_test.dart
	//   - C / C++   test_*.{c,cc,cpp,cxx}, *_test.{c,cc,cpp,cxx}
	//   - Erlang    *_test.erl, *_tests.erl, *_SUITE.erl (Common Test)
	pathRETest = regexp.MustCompile(`(?i)(^|/)(` +
		// Generic test directories — lang-agnostic.
		`(__tests__|tests?|specs?|e2e|fixtures?|testdata)(/|$)` +
		// Go / Python / Ruby / Rust / Elixir / Dart (suffix _test or _spec).
		`|.*(_test|_spec)\.(go|py|rb|rs|exs|dart)$` +
		// JS / TS family.
		`|.*\.(test|spec)\.(js|jsx|ts|tsx|mjs|cjs)$` +
		// Python prefix style.
		`|test_[^/]+\.py$` +
		// Swift.
		`|.*Tests?\.swift$` +
		// JVM family + C#.
		`|.*Test\.(java|kt|scala|cs)$` +
		// PHP — both PascalCase and snake_case conventions.
		`|.*Test\.php$|.*_test\.php$` +
		// C / C++ — both gtest "test_X.cpp" and Catch2 "X_test.cpp".
		`|test_[^/]+\.(c|cc|cpp|cxx)$|.*_test\.(c|cc|cpp|cxx)$` +
		// Erlang — EUnit (_test, _tests) + Common Test (_SUITE).
		`|.*_(tests?|SUITE)\.erl$` +
		`)`)

	// Compatibility / shim directories. The heuristic only fires on
	// the directory itself — `compat.go` (single file) is not enough,
	// but `compat/` or `legacy/` is. Polyfill is the dominant JS
	// convention; backport is the dominant Python convention.
	pathRECompat = regexp.MustCompile(`(?i)(^|/)(compat|legacy|polyfill|polyfills|shim|shims|backport|backports|deprecated)(/|$)`)

	// Examples / demo trees. Same dir-level rule: a file called
	// `example.go` (a single module) is not enough; `examples/` or
	// `demo/` directories are.
	pathREExamples = regexp.MustCompile(`(?i)(^|/)(examples?|demos?|samples?|sandbox|playground)(/|$)`)

	// Type declarations — interface files that don't carry the
	// implementation. TS *.d.ts is the canonical case; Python's .pyi
	// stub mirror; C/C++ headers (.h, .hpp, .hh, .hxx) when they're
	// in include/ or directly named like a type-only declaration.
	pathRETypeDecl = regexp.MustCompile(`(?i)\.(d\.ts|d\.cts|d\.mts|pyi|hpp|hxx|hh)$|(^|/)include/.*\.h$`)

	// Re-export filenames — barrels that just forward symbols from
	// other modules. The canonical names across ecosystems.
	reexportNames = map[string]struct{}{
		"index.js":  {},
		"index.jsx": {},
		"index.ts":  {},
		"index.tsx": {},
		"index.mjs": {},
		"index.cjs": {},
		"__init__.py": {},
		"mod.rs": {},
		"lib.rs": {},
	}
)

// supportDemoteTest is the multiplicative factor applied to a test-file
// candidate's final rerank score. The additive PathPenaltySignal is only
// a gentle tie-breaker (~0.12 score points); it cannot demote a test
// file that out-BM25s the real implementation on shared vocabulary —
// common for intent queries, where a test name echoes the feature it
// exercises ("test_urlencoded_data" for "urlencode form body payload").
// Multiplying the whole score reliably pushes such a test below the
// implementation. Only test files are demoted this hard; the lighter
// supporting-cast buckets keep the gentle additive signal.
const supportDemoteTest = 0.5

// supportFileDemotion returns the multiplicative score factor for a
// candidate's file — supportDemoteTest for a test file, 1.0 otherwise.
// It reuses the per-Rerank path-penalty cache the additive signal
// already populated, falling back to the classifier only when the cache
// is cold, so the regex rubric runs at most once per file per Rerank.
func supportFileDemotion(c *Candidate, ctx *Context) float64 {
	if c == nil || c.Node == nil || c.Node.FilePath == "" {
		return 1.0
	}
	// Only concept / degraded-identifier queries are demoted. An
	// identifier, path, or signature query carries an exact-token match
	// that already puts the right symbol on top; perturbing that order
	// to demote a test can only cost a name-level hit, and a user who
	// types a literal test name wants the test. Test pollution is a
	// natural-language-intent problem, so the demotion is scoped to it.
	if ctx != nil {
		switch ctx.QueryClass {
		case QueryClassSymbol, QueryClassPath, QueryClassSignature:
			return 1.0
		}
	}
	fp := c.Node.FilePath
	var pen float64
	if ctx != nil && ctx.pathPenaltyCache != nil {
		if cached, ok := ctx.pathPenaltyCache[fp]; ok {
			pen = cached
		} else {
			pen = classifyPathPenalty(fp)
		}
	} else {
		pen = classifyPathPenalty(fp)
	}
	if pen == PathPenaltyTest {
		return supportDemoteTest
	}
	return 1.0
}

// classifyPathPenalty applies the rubric in priority order — most
// aggressive penalty wins on overlap. Exported indirectly via the
// signal so tests can pin specific paths.
func classifyPathPenalty(fp string) float64 {
	// Normalise to forward slashes so the regexes are platform-stable.
	norm := strings.ReplaceAll(fp, "\\", "/")
	if pathRETest.MatchString(norm) {
		return PathPenaltyTest
	}
	if pathRECompat.MatchString(norm) {
		return PathPenaltyCompat
	}
	if pathREExamples.MatchString(norm) {
		return PathPenaltyExamples
	}
	if pathRETypeDecl.MatchString(norm) {
		return PathPenaltyTypeDecl
	}
	base := path.Base(norm)
	if _, ok := reexportNames[strings.ToLower(base)]; ok {
		return PathPenaltyReexport
	}
	return PathPenaltyUncatched
}
