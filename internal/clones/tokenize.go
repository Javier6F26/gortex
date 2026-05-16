package clones

import (
	"strings"
	"unicode/utf8"
)

// universalKeywords is a deliberately broad, language-agnostic set of
// control-flow and declaration keywords. Tokens in this set are kept
// verbatim during normalisation so the structural skeleton of a
// function body survives; every other identifier collapses to the
// placeholder "v". A language whose exotic keyword is missing here
// simply has that keyword normalised to "v" — detection degrades
// gracefully rather than breaking.
var universalKeywords = map[string]struct{}{
	// conditionals / loops
	"if": {}, "else": {}, "elif": {}, "elsif": {}, "unless": {},
	"for": {}, "while": {}, "do": {}, "loop": {}, "foreach": {},
	"switch": {}, "case": {}, "default": {}, "match": {}, "when": {},
	"break": {}, "continue": {}, "goto": {}, "return": {}, "yield": {},
	// declarations
	"func": {}, "function": {}, "fn": {}, "def": {}, "fun": {}, "sub": {}, "proc": {},
	"class": {}, "struct": {}, "interface": {}, "enum": {}, "trait": {},
	"impl": {}, "type": {}, "typedef": {}, "record": {}, "module": {}, "package": {},
	"var": {}, "let": {}, "const": {}, "final": {}, "val": {}, "static": {}, "mut": {},
	"public": {}, "private": {}, "protected": {}, "internal": {}, "abstract": {},
	"export": {}, "import": {}, "namespace": {}, "use": {}, "using": {}, "from": {},
	// objects
	"new": {}, "delete": {}, "this": {}, "self": {}, "super": {}, "extends": {},
	"implements": {}, "override": {}, "virtual": {},
	// errors / exceptions
	"try": {}, "catch": {}, "finally": {}, "throw": {}, "throws": {},
	"raise": {}, "except": {}, "rescue": {}, "ensure": {}, "defer": {}, "panic": {},
	"recover": {},
	// concurrency
	"async": {}, "await": {}, "go": {}, "chan": {}, "select": {}, "spawn": {},
	"synchronized": {}, "volatile": {},
	// boolean / logical
	"true": {}, "false": {}, "nil": {}, "null": {}, "none": {}, "undefined": {},
	"void": {}, "and": {}, "or": {}, "not": {}, "in": {}, "is": {}, "as": {},
	"of": {}, "with": {}, "where": {}, "then": {}, "begin": {}, "end": {},
	"lambda": {}, "where_": {},
}

// operatorRunChars are the punctuation characters that can chain into a
// single multi-character operator token (==, !=, :=, <=, &&, ->, =>,
// ::, ++, <<, etc.). Brackets, braces, parentheses, commas and
// semicolons are intentionally excluded — each is its own single-char
// token so call/block structure stays granular.
const operatorRunChars = "+-*/%=<>!&|^~?:.@"

// Tokenize reduces a source body to a normalised, language-agnostic
// token stream:
//
//   - identifier in universalKeywords  → the lower-cased keyword
//   - any other identifier             → "v"
//   - numeric literal                  → "0"
//   - string / char / raw literal      → "s"
//   - run of operator characters       → the verbatim run (e.g. "==")
//   - single bracket / brace / paren / comma / semicolon → itself
//
// Whitespace is dropped. Comments are not stripped — copy-pasted
// clones carry copy-pasted comments, and the Jaccard threshold absorbs
// the occasional divergence. The result is deterministic and depends
// only on the input bytes.
func Tokenize(body string) []string {
	// Capacity hint sized for typical code density (~1 token per 8
	// bytes for ordinary identifier-heavy source). Smaller estimate
	// than the previous len/4 hint, which over-allocated by ~2× on
	// the median function body; append will grow the slice geometrically
	// when bodies tokenize denser than expected.
	tokens := make([]string, 0, len(body)/8+8)
	n := len(body)
	i := 0
	for i < n {
		// Decode one rune in place rather than materialising the whole
		// body as a []rune up front — that bulk conversion was the
		// single biggest allocation in this function (590 MB / 30 s
		// during cold-start indexing in profile #3).
		c, size := utf8.DecodeRuneInString(body[i:])
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i += size
		case isIdentStart(c):
			j := i + size
			for j < n {
				rr, rsize := utf8.DecodeRuneInString(body[j:])
				if !isIdentPart(rr) {
					break
				}
				j += rsize
			}
			// body[i:j] is a zero-copy substring (shares body's
			// underlying bytes) — no allocation here. strings.ToLower
			// only allocates when the slice actually has uppercase
			// content, so all-lowercase keywords stay free.
			word := body[i:j]
			lower := strings.ToLower(word)
			if _, ok := universalKeywords[lower]; ok {
				tokens = append(tokens, lower)
			} else {
				tokens = append(tokens, "v")
			}
			i = j
		case c >= '0' && c <= '9':
			j := i + size
			for j < n {
				rr, rsize := utf8.DecodeRuneInString(body[j:])
				if !isNumberPart(rr) {
					break
				}
				j += rsize
			}
			tokens = append(tokens, "0")
			i = j
		case c == '"' || c == '\'' || c == '`':
			i = skipStringLiteral(body, i)
			tokens = append(tokens, "s")
		case strings.ContainsRune(operatorRunChars, c):
			j := i + size
			for j < n {
				rr, rsize := utf8.DecodeRuneInString(body[j:])
				if !strings.ContainsRune(operatorRunChars, rr) {
					break
				}
				j += rsize
			}
			tokens = append(tokens, body[i:j])
			i = j
		case c == '(' || c == ')' || c == '[' || c == ']' ||
			c == '{' || c == '}' || c == ',' || c == ';':
			tokens = append(tokens, body[i:i+size])
			i += size
		default:
			// Unknown punctuation / non-ASCII symbol — keep it as a
			// single token so it still contributes to the shape.
			tokens = append(tokens, body[i:i+size])
			i += size
		}
	}
	return tokens
}

// skipStringLiteral returns the byte index just past a string, char,
// or raw literal starting at body[start]. Backslash escapes are
// honoured for "/' quotes; backtick raw strings run to the next
// backtick. An unterminated literal consumes to end-of-input.
//
// Operates on the source bytes directly via utf8.DecodeRuneInString so
// no []rune intermediate is needed — mirrors Tokenize's per-rune
// streaming approach.
func skipStringLiteral(body string, start int) int {
	quote, qsize := utf8.DecodeRuneInString(body[start:])
	i := start + qsize
	n := len(body)
	for i < n {
		c, csize := utf8.DecodeRuneInString(body[i:])
		if c == '\\' && quote != '`' {
			// Skip the backslash and whatever rune it escapes.
			i += csize
			if i < n {
				_, esize := utf8.DecodeRuneInString(body[i:])
				i += esize
			}
			continue
		}
		if c == quote {
			return i + csize
		}
		i += csize
	}
	return n
}

func isIdentStart(c rune) bool {
	return c == '_' || c == '$' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		c > 127
}

func isIdentPart(c rune) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

func isNumberPart(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') ||
		(c >= 'A' && c <= 'F') || c == '.' || c == '_' ||
		c == 'x' || c == 'X' || c == 'o' || c == 'O' ||
		c == 'b' || c == 'B' || c == 'e' || c == 'E'
}
