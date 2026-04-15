package contracts

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/zzet/gortex/internal/graph"
)

// HTTPExtractor detects HTTP route provider and consumer patterns across
// multiple languages using regex matching on source text.
type HTTPExtractor struct{}

var _ Extractor = (*HTTPExtractor)(nil)

// SupportedLanguages returns the languages this extractor can analyse.
func (h *HTTPExtractor) SupportedLanguages() []string {
	return []string{"go", "typescript", "javascript", "python", "java", "dart"}
}

// httpPattern describes a single regex pattern that matches an HTTP route
// declaration or call.
type httpPattern struct {
	re        *regexp.Regexp
	role      Role
	method    string // HTTP method (empty = extract from match)
	methodGrp int    // capture group index for method when not fixed
	pathGrp   int    // capture group index for path
	// handlerGrp is the capture group for the handler identifier on the
	// provider side (e.g. `listUsers` in `r.GET("/users", listUsers)`).
	// 0 = not captured. When set and the capture resolves to a function
	// node in the same file, the Contract's SymbolID is the handler, not
	// the enclosing registration function — so "trace a request" queries
	// land on the business logic instead of setupRoutes().
	handlerGrp int
	framework  string
	confidence float64
	languages  []string // empty = all
}

// Compiled patterns -----------------------------------------------------------

var httpPatterns = []httpPattern{
	// ---- Go providers (high confidence, framework-specific) ----
	// Go 1.22+ stdlib mux: mux.HandleFunc("METHOD /path", h). The
	// method is embedded in the pattern as a prefix and must be
	// split out so the resulting contract ID matches the consumer
	// side's http::METHOD::path shape.
	{
		re:         regexp.MustCompile(`(?:Handle|HandleFunc)\(\s*["` + "`" + `](GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS)\s+(/[^"` + "`" + `]*)["` + "`" + `]\s*(?:,\s*(\w+))?`),
		role:       RoleProvider,
		methodGrp:  1,
		pathGrp:    2,
		handlerGrp: 3,
		framework:  "net/http",
		confidence: 0.95,
		languages:  []string{"go"},
	},
	// Legacy net/http HandleFunc with pattern-only path. Requires the
	// captured path to start with "/" (no leading verb), so the Go
	// 1.22+ "METHOD /path" form above doesn't double-match and emit
	// a bogus http::ANY::/VERB path contract alongside the canonical
	// http::VERB::/path one.
	{
		re:         regexp.MustCompile(`(?:Handle|HandleFunc)\(\s*["` + "`" + `](/[^"` + "`" + `]*)["` + "`" + `]\s*(?:,\s*(\w+))?`),
		role:       RoleProvider,
		method:     "ANY",
		pathGrp:    1,
		handlerGrp: 2,
		framework:  "net/http",
		confidence: 0.9,
		languages:  []string{"go"},
	},
	{
		// Match router/group method calls but not http.Get/http.Post (stdlib consumers).
		re:         regexp.MustCompile(`(?:^|[^/])\b(?:r|g|e|router|group|api|v1|mux|app)\.(Get|Post|Put|Delete|Patch|Head|Options)\(\s*["` + "`" + `]([^"` + "`" + `]+)["` + "`" + `]\s*(?:,\s*(\w+))?`),
		role:       RoleProvider,
		methodGrp:  1,
		pathGrp:    2,
		handlerGrp: 3,
		framework:  "gin/echo/chi",
		confidence: 0.9,
		languages:  []string{"go"},
	},
	{
		re:         regexp.MustCompile(`\.(GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS)\(\s*["` + "`" + `]([^"` + "`" + `]+)["` + "`" + `]\s*(?:,\s*(\w+))?`),
		role:       RoleProvider,
		methodGrp:  1,
		pathGrp:    2,
		handlerGrp: 3,
		framework:  "fiber",
		confidence: 0.9,
		languages:  []string{"go"},
	},

	// ---- TS/JS providers ----
	{
		re:         regexp.MustCompile(`(?:app|router)\.(get|post|put|delete|patch|head|options|all)\(\s*["'` + "`" + `]([^"'` + "`" + `]+)["'` + "`" + `]\s*(?:,\s*(\w+))?`),
		role:       RoleProvider,
		methodGrp:  1,
		pathGrp:    2,
		handlerGrp: 3,
		framework:  "express",
		confidence: 0.9,
		languages:  []string{"typescript", "javascript"},
	},
	{
		re:         regexp.MustCompile(`@(Get|Post|Put|Delete|Patch|Head|Options)\(\s*["'` + "`" + `]([^"'` + "`" + `]+)["'` + "`" + `]`),
		role:       RoleProvider,
		methodGrp:  1,
		pathGrp:    2,
		framework:  "nestjs",
		confidence: 0.9,
		languages:  []string{"typescript", "javascript"},
	},

	// ---- Python providers ----
	{
		re:         regexp.MustCompile(`@\w+\.(get|post|put|delete|patch|head|options)\(\s*["']([^"']+)["']`),
		role:       RoleProvider,
		methodGrp:  1,
		pathGrp:    2,
		framework:  "fastapi/flask",
		confidence: 0.9,
		languages:  []string{"python"},
	},
	{
		re:         regexp.MustCompile(`@\w+\.route\(\s*["']([^"']+)["']`),
		role:       RoleProvider,
		method:     "ANY",
		pathGrp:    1,
		framework:  "flask",
		confidence: 0.9,
		languages:  []string{"python"},
	},
	{
		re:         regexp.MustCompile(`path\(\s*["']([^"']+)["']`),
		role:       RoleProvider,
		method:     "ANY",
		pathGrp:    1,
		framework:  "django",
		confidence: 0.7,
		languages:  []string{"python"},
	},

	// ---- Java providers ----
	{
		re:         regexp.MustCompile(`@(Get|Post|Put|Delete|Patch)Mapping\(\s*(?:value\s*=\s*)?["']([^"']+)["']`),
		role:       RoleProvider,
		methodGrp:  1,
		pathGrp:    2,
		framework:  "spring",
		confidence: 0.9,
		languages:  []string{"java"},
	},
	{
		re:         regexp.MustCompile(`@RequestMapping\(\s*(?:value\s*=\s*)?["']([^"']+)["']`),
		role:       RoleProvider,
		method:     "ANY",
		pathGrp:    1,
		framework:  "spring",
		confidence: 0.9,
		languages:  []string{"java"},
	},
	{
		re:         regexp.MustCompile(`@(GET|POST|PUT|DELETE|PATCH)\s+@Path\(\s*["']([^"']+)["']`),
		role:       RoleProvider,
		methodGrp:  1,
		pathGrp:    2,
		framework:  "jaxrs",
		confidence: 0.9,
		languages:  []string{"java"},
	},

	// ---- Go consumers ----
	{
		re:         regexp.MustCompile(`http\.(Get|Post|Head)\(\s*["` + "`" + `]([^"` + "`" + `]+)["` + "`" + `]`),
		role:       RoleConsumer,
		methodGrp:  1,
		pathGrp:    2,
		framework:  "net/http",
		confidence: 0.9,
		languages:  []string{"go"},
	},
	{
		re:         regexp.MustCompile(`http\.NewRequest\(\s*["` + "`" + `](\w+)["` + "`" + `]\s*,\s*["` + "`" + `]([^"` + "`" + `]+)["` + "`" + `]`),
		role:       RoleConsumer,
		methodGrp:  1,
		pathGrp:    2,
		framework:  "net/http",
		confidence: 0.9,
		languages:  []string{"go"},
	},

	// ---- TS/JS consumers ----
	{
		re:         regexp.MustCompile(`fetch\(\s*["'` + "`" + `]([^"'` + "`" + `]+)["'` + "`" + `]`),
		role:       RoleConsumer,
		method:     "GET",
		pathGrp:    1,
		framework:  "fetch",
		confidence: 0.7,
		languages:  []string{"typescript", "javascript"},
	},
	{
		re:         regexp.MustCompile(`axios\.(get|post|put|delete|patch|head|options)\(\s*["'` + "`" + `]([^"'` + "`" + `]+)["'` + "`" + `]`),
		role:       RoleConsumer,
		methodGrp:  1,
		pathGrp:    2,
		framework:  "axios",
		confidence: 0.9,
		languages:  []string{"typescript", "javascript"},
	},

	// ---- Python consumers ----
	{
		re:         regexp.MustCompile(`(?:requests|httpx)\.(get|post|put|delete|patch|head|options)\(\s*["']([^"']+)["']`),
		role:       RoleConsumer,
		methodGrp:  1,
		pathGrp:    2,
		framework:  "requests/httpx",
		confidence: 0.9,
		languages:  []string{"python"},
	},

	// ---- Java consumers (generic) ----
	{
		re:         regexp.MustCompile(`(?:HttpClient|RestTemplate|WebClient).*["']([^"']+)["']`),
		role:       RoleConsumer,
		method:     "GET",
		pathGrp:    1,
		framework:  "java-http",
		confidence: 0.7,
		languages:  []string{"java"},
	},

	// ---- Dart consumers ----
	// Dio (the dominant HTTP client in modern Flutter apps). Matches
	// identifiers like `dio`, `_dio`, `apiDio` etc. invoking a method
	// with a string-literal path.
	{
		re:         regexp.MustCompile(`\b_?\w*[Dd]io\.(get|post|put|delete|patch|head)\(\s*['"]([^'"]+)['"]`),
		role:       RoleConsumer,
		methodGrp:  1,
		pathGrp:    2,
		framework:  "dio",
		confidence: 0.9,
		languages:  []string{"dart"},
	},
	// package:http functional API — http.get(Uri.parse('/x')) or
	// http.post('/x'). The regex captures either the string inside
	// Uri.parse or the direct literal argument.
	{
		re:         regexp.MustCompile(`\bhttp\.(get|post|put|delete|patch|head)\(\s*(?:Uri\.parse\(\s*)?['"]([^'"]+)['"]`),
		role:       RoleConsumer,
		methodGrp:  1,
		pathGrp:    2,
		framework:  "package:http",
		confidence: 0.8,
		languages:  []string{"dart"},
	},
}

// Extract scans src for HTTP route patterns and returns contracts.
func (h *HTTPExtractor) Extract(filePath string, src []byte, nodes []*graph.Node, edges []*graph.Edge) []Contract {
	text := string(src)
	lines := strings.Split(text, "\n")

	// Pre-sort file nodes by start line for enclosing-function lookup.
	fileNodes := filterFileNodes(filePath, nodes)
	sort.Slice(fileNodes, func(i, j int) bool {
		return fileNodes[i].StartLine < fileNodes[j].StartLine
	})

	lang := detectLanguage(filePath)

	var out []Contract

	for _, pat := range httpPatterns {
		if !patternMatchesLang(pat, lang) {
			continue
		}
		for _, m := range pat.re.FindAllStringSubmatchIndex(text, -1) {
			lineNum := lineAtOffset(lines, m[0])
			method := pat.method
			path := ""

			if pat.methodGrp > 0 {
				method = strings.ToUpper(text[m[pat.methodGrp*2]:m[pat.methodGrp*2+1]])
			}
			path = text[m[pat.pathGrp*2]:m[pat.pathGrp*2+1]]

			normPath := NormalizeHTTPPath(path)
			contractID := fmt.Sprintf("http::%s::%s", method, normPath)

			symbolID := findEnclosingSymbol(fileNodes, lineNum)

			// Provider patterns that also capture the handler identifier
			// re-point SymbolID at the actual handler function in the
			// same file. Two forms handled:
			//   1. Bare handler:  r.GET("/users", listUsers)
			//      → handlerGrp captures "listUsers", resolve directly.
			//   2. Middleware-wrapped: mux.HandleFunc("POST /x",
			//      WithAuth(auth, h.CreateTuck)) — handlerGrp grabs
			//      "WithAuth" which is a wrapper. Walk forward from
			//      the end of the handlerGrp match, through the rest
			//      of the call's balanced parens, and pick the LAST
			//      identifier (or method reference like h.CreateTuck)
			//      that resolves to a function in this file. That's
			//      the innermost handler — what "trace a request"
			//      actually wants to land on.
			if pat.handlerGrp > 0 && pat.role == RoleProvider {
				gStart := m[pat.handlerGrp*2]
				gEnd := m[pat.handlerGrp*2+1]
				if gStart >= 0 && gEnd > gStart {
					handlerName := text[gStart:gEnd]
					if hID := resolveHandlerIdent(fileNodes, handlerName); hID != "" {
						symbolID = hID
					} else {
						// Scan the call-trail for a better candidate.
						trail := callTrailSlice(text, m[1])
						if hID := findInnermostResolvableHandler(fileNodes, trail); hID != "" {
							symbolID = hID
						}
					}
				}
			}

			c := Contract{
				ID:       contractID,
				Type:     ContractHTTP,
				Role:     pat.role,
				SymbolID: symbolID,
				FilePath: filePath,
				Line:     lineNum,
				Meta: map[string]any{
					"method":    method,
					"path":      normPath,
					"framework": pat.framework,
				},
				Confidence: pat.confidence,
			}
			out = append(out, c)
		}
	}

	return out
}

// detectLanguage infers the language from a file extension.
func detectLanguage(filePath string) string {
	switch {
	case strings.HasSuffix(filePath, ".go"):
		return "go"
	case strings.HasSuffix(filePath, ".ts"), strings.HasSuffix(filePath, ".tsx"):
		return "typescript"
	case strings.HasSuffix(filePath, ".js"), strings.HasSuffix(filePath, ".jsx"):
		return "javascript"
	case strings.HasSuffix(filePath, ".py"):
		return "python"
	case strings.HasSuffix(filePath, ".java"):
		return "java"
	case strings.HasSuffix(filePath, ".dart"):
		return "dart"
	default:
		return ""
	}
}

// patternMatchesLang returns true if the pattern applies to the given language.
func patternMatchesLang(p httpPattern, lang string) bool {
	if len(p.languages) == 0 {
		return true
	}
	for _, l := range p.languages {
		if l == lang {
			return true
		}
	}
	return false
}

// lineAtOffset returns the 1-based line number for the given byte offset.
func lineAtOffset(lines []string, offset int) int {
	pos := 0
	for i, l := range lines {
		end := pos + len(l) + 1 // +1 for newline
		if offset < end {
			return i + 1
		}
		pos = end
	}
	return len(lines)
}

// filterFileNodes returns only nodes that belong to the given file.
func filterFileNodes(filePath string, nodes []*graph.Node) []*graph.Node {
	var out []*graph.Node
	for _, n := range nodes {
		if n.FilePath == filePath {
			out = append(out, n)
		}
	}
	return out
}

// findEnclosingSymbol returns the ID of the nearest function/method that
// encloses the given line number.  Falls back to "" if none found.
//
// Strict containment (StartLine ≤ line ≤ EndLine) is preferred, but some
// language extractors (notably Dart's tree-sitter path) report EndLine as
// the signature line rather than the closing brace, so a call on the very
// next line wouldn't match. When strict containment fails, fall back to
// the closest-preceding symbol whose EndLine ≥ (line - closeProximity) —
// the call is most likely inside its body. "" still means nothing's even
// near enough.
func findEnclosingSymbol(sortedNodes []*graph.Node, line int) string {
	best := ""
	bestStart := 0
	for _, n := range sortedNodes {
		if n.Kind != graph.KindFunction && n.Kind != graph.KindMethod {
			continue
		}
		if n.StartLine <= line && n.EndLine >= line && n.StartLine >= bestStart {
			best = n.ID
			bestStart = n.StartLine
		}
	}
	if best != "" {
		return best
	}
	// Fallback: the closest function/method whose declaration precedes
	// the line — tolerates off-by-N EndLine reports from extractors that
	// don't compute the closing brace.
	fallback := ""
	fallbackStart := 0
	for _, n := range sortedNodes {
		if n.Kind != graph.KindFunction && n.Kind != graph.KindMethod {
			continue
		}
		if n.StartLine <= line && n.StartLine > fallbackStart {
			fallback = n.ID
			fallbackStart = n.StartLine
		}
	}
	return fallback
}

// findFunctionByName returns the ID of a function or method declared in the
// same file with the given short name (e.g. "listUsers"). Used by the HTTP
// provider extractor to re-point a contract's SymbolID at its handler
// function when the pattern captures it.
func findFunctionByName(fileNodes []*graph.Node, name string) string {
	for _, n := range fileNodes {
		if n.Kind != graph.KindFunction && n.Kind != graph.KindMethod {
			continue
		}
		if n.Name == name {
			return n.ID
		}
	}
	return ""
}

// resolveHandlerIdent resolves a handler identifier captured by a
// provider-pattern regex. Accepts bare "listUsers" (function name)
// and method-expression "h.CreateTuck" (dot-qualified) — the latter
// common when routes are registered on a receiver. The method-name
// after the dot is used for the lookup, so `h.CreateTuck` resolves
// to a method CreateTuck in the same file regardless of receiver
// variable name.
func resolveHandlerIdent(fileNodes []*graph.Node, ident string) string {
	if ident == "" {
		return ""
	}
	if i := strings.LastIndex(ident, "."); i >= 0 {
		ident = ident[i+1:]
	}
	return findFunctionByName(fileNodes, ident)
}

// callTrailSlice returns the byte slice that starts at the HandleFunc
// call's opening "(" (found by the regex at matchStart) and ends at
// the matching balanced close ")". Used to scan past a middleware
// wrapper for an inner handler identifier. Returns empty when the
// call can't be balanced (which only happens on truncated or invalid
// source — production files are fine).
func callTrailSlice(src string, matchStart int) string {
	// Seek forward from matchStart to the first '(' — that's the
	// opening paren of the HandleFunc call. The regex's m[0] lands
	// at the start of the "HandleFunc" token.
	openIdx := -1
	for i := matchStart; i < len(src); i++ {
		if src[i] == '(' {
			openIdx = i
			break
		}
		if src[i] == '\n' {
			return ""
		}
	}
	if openIdx < 0 {
		return ""
	}
	depth := 0
	i := openIdx
	for i < len(src) {
		switch src[i] {
		case '(':
			depth++
			i++
		case ')':
			depth--
			if depth == 0 {
				return src[openIdx+1 : i]
			}
			i++
		case '"', '\'', '`':
			q := src[i]
			i++
			for i < len(src) && src[i] != q {
				if src[i] == '\\' && i+1 < len(src) {
					i += 2
					continue
				}
				i++
			}
			if i < len(src) {
				i++
			}
		default:
			i++
		}
	}
	return ""
}

// handlerCandidateRE captures every bare identifier or `recv.Method`
// style expression in the call-trail. Tight enough to skip keywords
// like "context" or "nil" only by not resolving them to a file-local
// function — the caller filters via findFunctionByName.
var handlerCandidateRE = regexp.MustCompile(`\b([A-Za-z_]\w*(?:\.\w+)?)\b`)

// findInnermostResolvableHandler walks the call trail and returns the
// LAST identifier that resolves to a function or method declared in
// the same file. For `WithAuth(auth, h.CreateTuck)` this is
// `h.CreateTuck` (resolves to CreateTuck method); WithAuth and auth
// fail to resolve (not file-local). Returns "" if no candidate
// resolves.
func findInnermostResolvableHandler(fileNodes []*graph.Node, trail string) string {
	matches := handlerCandidateRE.FindAllStringSubmatch(trail, -1)
	var best string
	for _, m := range matches {
		if id := resolveHandlerIdent(fileNodes, m[1]); id != "" {
			best = id
		}
	}
	return best
}
