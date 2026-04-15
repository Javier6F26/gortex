package contracts

import (
	"regexp"
	"strings"
)

// argKind classifies the first argument passed to a wrapper call.
type argKind int

const (
	argUnknown   argKind = iota // non-literal, non-identifier (runtime expression)
	argLiteral                  // string/template literal — the caller's specific path
	argBareParam                // bare identifier (likely the caller's own path parameter)
)

// extractedArg describes the first argument at a wrapper call site
// plus any HTTP method found on a later argument's method: property.
type extractedArg struct {
	Kind   argKind
	Value  string // populated for argLiteral
	Method string // e.g. "POST"; empty means "unknown / default GET"
}

// extractFirstCallArg finds the call expression at the given 1-based
// line in src and returns the first argument's classification plus any
// method: property from a later argument. wrapperName is the short
// name of the function being called (e.g. "request"). lang is the
// caller's language ("typescript", "javascript", "go", "dart").
//
// The strategy is the same across languages because call syntax is
// uniform enough in practice: find wrapperName(, scan forward until
// the matching close-paren while tracking paren depth and string
// escapes, then parse the captured arg list. Works for:
//
//	request('/v1/users', getToken, { method: 'POST' })
//	request(`/v1/users/${id}`, getToken)
//	doFetch(path, token, options)
//	request("/v1/users", getToken, map[string]any{"method": "POST"})
//
// Language only matters for literal-delimiter set (Go doesn't use
// backticks for template literals; TS does; etc.) and for the
// identifier-name shape, which defaults to the same pattern across
// the languages we target.
func extractFirstCallArg(src []byte, line int, wrapperName, lang string) extractedArg {
	if len(src) == 0 || wrapperName == "" || line <= 0 {
		return extractedArg{}
	}

	lineStart, lineEnd := lineBounds(src, line)
	if lineStart < 0 {
		return extractedArg{}
	}

	// Search for the wrapper name on the target line only. An edge's
	// Line is the line where the call expression starts; if the call
	// isn't mentioned on that line, we shouldn't be picking up an
	// unrelated call lower in the file.
	nameRE := regexp.MustCompile(`\b` + regexp.QuoteMeta(wrapperName) + `\b\s*(?:<[^>]*>\s*)?\(`)
	lineSlice := src[lineStart:lineEnd]
	loc := nameRE.FindIndex(lineSlice)
	if loc == nil {
		return extractedArg{}
	}
	// Arg list starts just after the opening '('. Use offsets into the
	// full source so balancedCloseParen can scan past line breaks for
	// multi-line argument lists.
	argStart := lineStart + loc[1]
	end := balancedCloseParen(src, argStart-1)
	if end < 0 {
		return extractedArg{}
	}
	argsText := string(src[argStart:end])

	first, rest := splitFirstArg(argsText)
	return extractedArg{
		Kind:   classifyArg(first, lang),
		Value:  unquoteIfLiteral(first),
		Method: findMethodProperty(rest),
	}
}

// lineBounds returns the 0-based byte offsets of the start and end of
// the given 1-based line. Returns -1 for lineStart if the line is out
// of range.
func lineBounds(src []byte, line int) (int, int) {
	current := 1
	start := 0
	for i, b := range src {
		if current == line {
			// Find end of this line.
			end := len(src)
			for j := i; j < len(src); j++ {
				if src[j] == '\n' {
					end = j
					break
				}
			}
			return start, end
		}
		if b == '\n' {
			current++
			start = i + 1
		}
	}
	if current == line {
		return start, len(src)
	}
	return -1, -1
}

// balancedCloseParen returns the offset of the ")" that matches the
// "(" at openIdx, respecting nested parens, string/template literals
// and comments. Returns -1 if no match is found.
func balancedCloseParen(src []byte, openIdx int) int {
	if openIdx < 0 || openIdx >= len(src) || src[openIdx] != '(' {
		return -1
	}
	depth := 0
	i := openIdx
	for i < len(src) {
		c := src[i]
		switch c {
		case '(':
			depth++
			i++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
			i++
		case '"', '\'', '`':
			// Skip until the matching close quote. Handles simple
			// escapes; template-literal ${...} interpolation is
			// treated as a string body (we don't recurse into it —
			// good enough for our call-site shapes).
			quote := c
			i++
			for i < len(src) && src[i] != quote {
				if src[i] == '\\' && i+1 < len(src) {
					i += 2
					continue
				}
				i++
			}
			if i < len(src) {
				i++ // consume close quote
			}
		case '/':
			// Skip // line comments and /* block comments.
			if i+1 < len(src) && src[i+1] == '/' {
				for i < len(src) && src[i] != '\n' {
					i++
				}
			} else if i+1 < len(src) && src[i+1] == '*' {
				i += 2
				for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
					i++
				}
				i += 2
			} else {
				i++
			}
		default:
			i++
		}
	}
	return -1
}

// splitFirstArg returns the first comma-separated argument and the
// remaining arg list as a single string, respecting nested parens,
// braces, brackets, and string literals.
func splitFirstArg(args string) (string, string) {
	depthParen, depthBrace, depthBracket := 0, 0, 0
	i := 0
	for i < len(args) {
		c := args[i]
		switch c {
		case '(':
			depthParen++
		case ')':
			depthParen--
		case '{':
			depthBrace++
		case '}':
			depthBrace--
		case '[':
			depthBracket++
		case ']':
			depthBracket--
		case '"', '\'', '`':
			quote := c
			i++
			for i < len(args) && args[i] != quote {
				if args[i] == '\\' && i+1 < len(args) {
					i += 2
					continue
				}
				i++
			}
		case ',':
			if depthParen == 0 && depthBrace == 0 && depthBracket == 0 {
				return strings.TrimSpace(args[:i]), strings.TrimSpace(args[i+1:])
			}
		}
		i++
	}
	return strings.TrimSpace(args), ""
}

// classifyArg decides whether an argument text is a literal string/
// template-literal (→ argLiteral), a bare identifier that looks like
// a function parameter (→ argBareParam), or something else.
func classifyArg(s, lang string) argKind {
	if s == "" {
		return argUnknown
	}
	// String literal (with or without quotes stripped earlier). The
	// caller strips nothing; we check here.
	if isQuoted(s) {
		// A template literal with interpolation is still a literal
		// for our purposes — NormalizeHTTPPath collapses ${id} to
		// {id}. But we exclude ones that are ENTIRELY a placeholder
		// ("`${url}`") because that's a wrapper forwarding the URL.
		inner := unquote(s)
		if isPureInterpolation(inner) {
			return argBareParam
		}
		return argLiteral
	}
	if isBareIdentifier(s) {
		return argBareParam
	}
	return argUnknown
}

// isQuoted reports whether s begins and ends with ", ', or `.
func isQuoted(s string) bool {
	if len(s) < 2 {
		return false
	}
	first, last := s[0], s[len(s)-1]
	return (first == '"' || first == '\'' || first == '`') && first == last
}

// unquote strips matching surrounding quotes.
func unquote(s string) string {
	if !isQuoted(s) {
		return s
	}
	return s[1 : len(s)-1]
}

// unquoteIfLiteral strips quotes when the arg looks like a literal,
// otherwise returns the arg unchanged. Non-literal returns are unused
// by the caller (the argKind guard skips them) but keeping the value
// debuggable is cheap.
func unquoteIfLiteral(s string) string {
	if isQuoted(s) {
		return unquote(s)
	}
	return s
}

// isPureInterpolation reports whether a template-literal body consists
// of a single ${ident} with nothing else. Those bodies mean "forward
// the URL parameter verbatim" — the caller is a wrapper, not a leaf.
var pureInterpRE = regexp.MustCompile(`^\$\{\s*[a-zA-Z_][a-zA-Z0-9_.]*\s*\}$`)

func isPureInterpolation(s string) bool {
	return pureInterpRE.MatchString(strings.TrimSpace(s))
}

// bareIdentRE accepts a single identifier possibly qualified with dots
// ("path", "opts.path") — Go / TS / JS / Dart all use the same shape.
var bareIdentRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(?:\.[a-zA-Z_][a-zA-Z0-9_]*)*$`)

func isBareIdentifier(s string) bool {
	return bareIdentRE.MatchString(s)
}

// findMethodProperty scans the remaining args for an object literal
// whose method: property names the HTTP method. Handles:
//
//	{ method: 'POST', ... }
//	{method:"PATCH"}
//	map[string]any{"method": "DELETE"}  // go-ish
//
// Returns the method in uppercase, or "" if none found.
// methodPropRE accepts both the TS/JS shorthand ( method: "POST" ) and
// the Go/JSON style ( "method": "POST" ) where the key is quoted.
var methodPropRE = regexp.MustCompile(`(?i)['"` + "`" + `]?\bmethod\b['"` + "`" + `]?\s*:\s*['"` + "`" + `]([A-Za-z]+)['"` + "`" + `]`)

func findMethodProperty(rest string) string {
	m := methodPropRE.FindStringSubmatch(rest)
	if len(m) < 2 {
		return ""
	}
	return strings.ToUpper(m[1])
}
