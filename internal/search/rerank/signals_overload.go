package rerank

import (
	"unicode"

	"github.com/zzet/gortex/internal/graph"
)

// OverloadProminenceSignal breaks ties among same-named candidates. When
// a query is ambiguous — several distinct symbols answer to one
// identifier (Go interface methods like BindBody across every binding,
// or a `decode` on each decoder class) — no ranker can know which
// overload the caller meant, but the likelier ones can be floated toward
// the top-5 by intrinsic prominence: an exported, non-test, callable
// definition is a better default answer than an unexported field or a
// test helper of the same name.
//
// The signal fires ONLY on a genuine same-name collision in the current
// batch (nameGroupCount > 1); a candidate whose name is unique
// contributes 0, so a non-ambiguous query is never perturbed. Every
// input — exported shape, test path, node kind — is an AST-tier fact
// available at index time with no enrichment, so the ordering is
// identical whether or not semantic enrichment has run.
type OverloadProminenceSignal struct{}

func (OverloadProminenceSignal) Name() string { return SignalOverload }

func (OverloadProminenceSignal) Contribute(query string, c *Candidate, ctx *Context) float64 {
	if c == nil || c.Node == nil || ctx == nil {
		return 0
	}
	nm := lowerName(c.Node.Name)
	if nm == "" {
		return 0
	}
	// Identifier query: fire only on a genuine same-name collision —
	// an exact-name lookup where overload disambiguation is the whole
	// problem. On any other class the batch's incidental same-name
	// pairs are noise, and a blanket prominence boost would fight the
	// semantic channel that is doing the real work.
	if ctx.QueryClass == QueryClassSymbol {
		if ctx.nameGroupCount[nm] <= 1 {
			return 0
		}
		return prominenceScore(c.Node)
	}
	// Degraded-identifier profile: a multi-word query whose tokens
	// concatenate to exactly this candidate's name is a case-split
	// identifier lookup ("should bind body with" → ShouldBindBodyWith),
	// not a concept query, for THIS candidate. Longer same-stem
	// variants (ShouldBindBodyWithJSON) share every query token and can
	// out-BM25 the exact match; the exact-match floor puts the queried
	// identifier back on top, with prominence breaking ties among
	// several exact matches (the overload group).
	if exactDegradedNameMatch(query, nm) {
		return 0.55 + 0.45*prominenceScore(c.Node)
	}
	return 0
}

// prominenceScore rates a symbol's intrinsic answer-worthiness in
// [0, 1] from AST-tier facts only.
func prominenceScore(n *graph.Node) float64 {
	var s float64
	if isLikelyExported(n) {
		s += 0.5
	}
	if !isTestPath(n.FilePath) {
		s += 0.3
	}
	switch n.Kind {
	case graph.KindFunction, graph.KindMethod, graph.KindType, graph.KindInterface:
		s += 0.2
	}
	if s > 1 {
		s = 1
	}
	return s
}

// exactDegradedNameMatch reports whether a multi-word query's tokens
// concatenate to exactly the (already-lowercased) candidate name,
// comparing alphanumeric runes only so snake_case names match their
// space-split degraded form too. Single-token queries return false —
// they are the symbol class's territory.
func exactDegradedNameMatch(query, lowerNodeName string) bool {
	qa := alnumFold(query, true)
	if qa == "" {
		return false
	}
	return qa == alnumFold(lowerNodeName, false)
}

// alnumFold lowercases and strips every non-alphanumeric rune. When
// requireMultiToken is set it returns "" unless the input had 2+
// whitespace-separated tokens (the degraded-identifier shape).
func alnumFold(s string, requireMultiToken bool) string {
	var b []rune
	tokens := 0
	inTok := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			inTok = false
			continue
		}
		if !inTok {
			tokens++
			inTok = true
		}
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b = append(b, r)
		case r >= 'A' && r <= 'Z':
			b = append(b, unicode.ToLower(r))
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b = append(b, unicode.ToLower(r))
		}
	}
	if requireMultiToken && tokens < 2 {
		return ""
	}
	return string(b)
}

// lowerName lowercases a symbol name without pulling in strings for a
// one-liner on the hot path. ASCII-fast, rune-correct for the rest.
func lowerName(s string) string {
	hasUpper := false
	for _, r := range s {
		if r >= 'A' && r <= 'Z' || unicode.IsUpper(r) {
			hasUpper = true
			break
		}
	}
	if !hasUpper {
		return s
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		out = append(out, unicode.ToLower(r))
	}
	return string(out)
}

// isLikelyExported reports whether a symbol is part of its module's
// public surface, using only the name shape and language — no
// enrichment. A leading underscore marks a private member in Python / JS
// conventions; Go (and the other case-visibility languages) key
// export on an uppercase initial; everything else is treated as public
// unless underscore-prefixed.
func isLikelyExported(n *graph.Node) bool {
	if n == nil || n.Name == "" {
		return false
	}
	first := []rune(n.Name)[0]
	if first == '_' {
		return false
	}
	switch n.Language {
	case "go", "java", "kotlin", "scala", "csharp":
		return unicode.IsUpper(first)
	}
	return true
}
