package contracts

import (
	"regexp"
	"strings"
)

// tsFieldRe matches a single member line inside a TS interface or
// type-literal:
//
//	name?: Type                      → optional
//	readonly name: Type              → required (readonly flag ignored)
//	'quoted-name': Type              → required, wire name is the literal
//	name: string | null              → optional (nullable union)
//	name: Array<Foo>                 → repeated
//
// Capture groups:
//  1: field name (may include quotes)
//  2: "?" when optional declared at the field level
//  3: type expression to the end of the line, trailing comma / semicolon trimmed
var tsFieldRe = regexp.MustCompile(`^\s*(?:readonly\s+)?(['"]?[\w$-]+['"]?)\s*(\??)\s*:\s*(.+?)\s*[,;]?\s*(?://\s*(.*))?$`)

// extractTSShape reads a TypeScript interface or type literal and
// returns its declared members.
//
// Supported shapes:
//
//	interface Foo { a: string; b?: number }
//	type Foo = { a: string; b?: number }
//	interface Foo extends Bar { a: string }
//	class Foo { a: string; b?: number; method(): void { ... } }
//
// Methods on classes are filtered out by the line pattern: lines
// containing `(` before the first `:` don't match tsFieldRe.
func extractTSShape(src []byte, startLine, endLine int) *Shape {
	body := sliceBody(src, startLine, endLine)
	if body == "" {
		body = braceBody(src, startLine, 300)
	}
	openIdx := strings.Index(body, "{")
	if openIdx < 0 {
		return nil
	}
	depth := 0
	closeIdx := -1
	for i := openIdx; i < len(body); i++ {
		switch body[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				closeIdx = i
			}
		}
		if closeIdx >= 0 {
			break
		}
	}
	if closeIdx < 0 {
		return nil
	}
	inner := body[openIdx+1 : closeIdx]

	kind := "interface"
	firstLine := body[:openIdx]
	if strings.Contains(firstLine, " type ") || strings.HasPrefix(strings.TrimSpace(firstLine), "type ") {
		kind = "type"
	} else if strings.Contains(firstLine, " class ") || strings.HasPrefix(strings.TrimSpace(firstLine), "class ") {
		kind = "class"
	}

	shape := &Shape{Kind: kind}
	for _, rawLine := range strings.Split(inner, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		// Skip method members: `foo(args): Ret { ... }` or `foo():
		// Ret` — the `(` appears before any `:`. An arrow-typed
		// property `foo: (a: A) => R` still keeps `:` first so it
		// matches as a field whose type happens to be a function —
		// that's fine, we record the type expression verbatim.
		colonIdx := strings.Index(line, ":")
		parenIdx := strings.Index(line, "(")
		if parenIdx >= 0 && (colonIdx < 0 || parenIdx < colonIdx) {
			continue
		}
		m := tsFieldRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		nameRaw := strings.Trim(m[1], `'"`)
		optional := m[2] == "?"
		typeExpr := strings.TrimSpace(m[3])
		comment := ""
		if len(m) > 4 {
			comment = truncateComment(m[4])
		}
		required := !optional && !tsTypeIsNullable(typeExpr)
		repeated := tsTypeIsRepeated(typeExpr)
		shape.Fields = append(shape.Fields, ShapeField{
			Name:     nameRaw,
			Type:     typeExpr,
			Required: required,
			Repeated: repeated,
			Comment:  comment,
		})
	}
	if len(shape.Fields) == 0 {
		return nil
	}
	return shape
}

// tsTypeIsNullable returns true for union types that include `null`
// or `undefined`. Those are optional on the wire even without the
// trailing `?` on the field name.
func tsTypeIsNullable(t string) bool {
	parts := strings.Split(t, "|")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "null" || p == "undefined" {
			return true
		}
	}
	return false
}

// tsTypeIsRepeated returns true for `Foo[]`, `Array<Foo>`, or
// `ReadonlyArray<Foo>`.
func tsTypeIsRepeated(t string) bool {
	t = strings.TrimSpace(t)
	if strings.HasSuffix(t, "[]") {
		return true
	}
	if strings.HasPrefix(t, "Array<") || strings.HasPrefix(t, "ReadonlyArray<") {
		return true
	}
	return false
}
