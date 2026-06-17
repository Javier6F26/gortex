package resolver

import "strings"

// cocoaPrepositions are the linking words the Objective-C → Swift importer
// treats as argument-label boundaries. When a selector's base keyword ends
// with one of these (optionally followed by the argument's type noun), the
// Swift method drops everything from the preposition onward from its base
// name and folds it into the first argument label: `cellForRowAtIndexPath:`
// surfaces in Swift as `cellForRow(at:)`, `moveFrom:to:` as `move(from:to:)`,
// `initWithFrame:` as `init(frame:)`.
var cocoaPrepositions = map[string]bool{
	"with": true, "for": true, "at": true, "by": true, "from": true,
	"to": true, "in": true, "into": true, "of": true, "on": true,
	"using": true, "as": true, "and": true, "about": true, "against": true,
	"around": true, "over": true, "under": true, "within": true, "before": true,
	"after": true, "between": true, "through": true, "until": true, "since": true,
}

// swiftObjCBaseNameCandidates derives the Swift method base names an
// Objective-C selector could surface under. It always yields the verbatim
// first keyword, plus — when that keyword ends in a Cocoa preposition phrase —
// the shortened base name the importer would produce. The result is the set a
// reverse bridge matches Swift method nodes against by name.
func swiftObjCBaseNameCandidates(selector string) []string {
	head := selector
	if i := strings.IndexByte(selector, ':'); i >= 0 {
		head = selector[:i]
	}
	head = strings.TrimSpace(head)
	if head == "" {
		return nil
	}

	out := []string{head}
	words := splitCamelWords(head)
	last := -1
	for i := 1; i < len(words); i++ {
		if cocoaPrepositions[strings.ToLower(words[i])] {
			last = i
		}
	}
	if last > 0 {
		if base := lowerFirstASCII(strings.Join(words[:last], "")); base != "" && base != head {
			out = append(out, base)
		}
	}
	return out
}

// splitCamelWords breaks a lowerCamelCase / UpperCamelCase identifier into its
// word components on lower→upper boundaries, keeping runs of capitals (e.g.
// acronyms) together: `cellForRowAtIndexPath` → [cell For Row At Index Path],
// `URLForKey` → [URL For Key].
func splitCamelWords(s string) []string {
	var words []string
	runes := []rune(s)
	start := 0
	for i := 1; i < len(runes); i++ {
		prev, cur := runes[i-1], runes[i]
		boundary := isLowerOrDigit(prev) && isUpper(cur)
		// End of an acronym run: ...URLFor → URL | For.
		if !boundary && isUpper(prev) && isUpper(cur) && i+1 < len(runes) && isLowerOrDigit(runes[i+1]) {
			boundary = true
		}
		if boundary {
			words = append(words, string(runes[start:i]))
			start = i
		}
	}
	words = append(words, string(runes[start:]))
	return words
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }

func isLowerOrDigit(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}

// lowerFirstASCII lowercases the first ASCII letter of s, leaving the rest
// intact, so a joined word-prefix reads as a Swift method base name.
func lowerFirstASCII(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'A' && r[0] <= 'Z' {
		r[0] = r[0] - 'A' + 'a'
	}
	return string(r)
}
