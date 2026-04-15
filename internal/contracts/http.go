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
	return []string{"go", "typescript", "javascript", "python", "java"}
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
	{
		re:         regexp.MustCompile(`(?:Handle|HandleFunc)\(\s*["` + "`" + `]([^"` + "`" + `]+)["` + "`" + `]\s*(?:,\s*(\w+))?`),
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
			// (e.g. `listUsers` in `r.GET("/users", listUsers)`) re-point
			// SymbolID at the handler when it resolves to a function/method
			// in the same file. This is T1.3: traversals that cross service
			// boundaries (via EdgeMatches) land on the business logic, not
			// on the registration helper whose line happens to enclose the
			// route declaration.
			if pat.handlerGrp > 0 && pat.role == RoleProvider {
				gStart := m[pat.handlerGrp*2]
				gEnd := m[pat.handlerGrp*2+1]
				if gStart >= 0 && gEnd > gStart {
					handlerName := text[gStart:gEnd]
					if hID := findFunctionByName(fileNodes, handlerName); hID != "" {
						symbolID = hID
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
	return best
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
