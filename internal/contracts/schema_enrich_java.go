package contracts

import (
	"regexp"
	"strings"

	"github.com/zzet/gortex/internal/graph"
)

// -----------------------------------------------------------------------------
// Java / Kotlin — Spring + JAX-RS providers
// -----------------------------------------------------------------------------
//
// Spring and JAX-RS are both decorator-heavy and typed, so we get good
// signal from the method signature: `@RequestBody FooDto body` pins
// the request type, the return type pins the response, and
// `@RequestParam`/`@PathVariable` enumerate query/path slots.
// `@ResponseStatus(HttpStatus.CREATED)` supplies status.

func init() {
	schemaEnrichers = append(schemaEnrichers,
		schemaEnricher{
			name:      "java-spring-provider",
			languages: []string{"java", "kotlin"},
			roles:     []Role{RoleProvider},
			detect:    javaSpringDetect,
		},
		schemaEnricher{
			name:      "java-jaxrs-provider",
			languages: []string{"java", "kotlin"},
			roles:     []Role{RoleProvider},
			detect:    javaJAXRSDetect,
		},
	)
}

// -----------------------------------------------------------------------------
// Spring provider
// -----------------------------------------------------------------------------

var (
	// Java-style: `@RequestBody FooDto foo` — annotation, then type,
	// then parameter name.
	springRequestBodyRe = regexp.MustCompile(`@RequestBody(?:\([^)]*\))?\s+(?:final\s+)?([A-Za-z_][\w<>.]*)\s+\w+`)
	// Kotlin-style: `@RequestBody foo: FooDto` — annotation, then
	// parameter name, colon, then type.
	kotlinRequestBodyRe = regexp.MustCompile(`@RequestBody(?:\([^)]*\))?\s+\w+\s*:\s*([A-Za-z_][\w<>.?]*)`)
	springRequestParam  = regexp.MustCompile(`@RequestParam(?:\(\s*(?:value\s*=\s*)?"([^"]+)")?`)
	// Kotlin-style: @RequestParam(value = "x") foo: Type
	springRespStatus = regexp.MustCompile(`@ResponseStatus\(\s*(?:value\s*=\s*)?(?:HttpStatus\.(\w+)|(\d+))`)
	// Spring method: returns ResponseEntity<FooDto> or FooDto directly.
	// Capture the declaration line of the method inside a controller.
	springMethodReturnRe = regexp.MustCompile(`(?:public|private|protected|fun)\s+(?:static\s+)?(?:final\s+)?([A-Za-z_][\w<>.]*)\s+\w+\s*\(`)
	// Kotlin: fun createUser(...): UserResp { ... }
	// `[^{]*` tolerates nested parens in annotations like
	// `@RequestParam("x")` inside the parameter list — we only stop
	// when we hit the method body's opening brace.
	kotlinFunReturnRe = regexp.MustCompile(`fun\s+\w+\s*\([^{]*\)\s*:\s*([A-Za-z_][\w<>.]*)\s*\{`)
)

func javaSpringDetect(body string, fileNodes []*graph.Node) schemaHints {
	var h schemaHints

	if m := springRequestBodyRe.FindStringSubmatch(body); len(m) > 1 {
		h.RequestType = resolveTypeInFile(stripGenerics(m[1]), fileNodes)
	} else if m := kotlinRequestBodyRe.FindStringSubmatch(body); len(m) > 1 {
		t := strings.TrimSuffix(stripGenerics(m[1]), "?")
		h.RequestType = resolveTypeInFile(t, fileNodes)
	}

	for _, m := range springRequestParam.FindAllStringSubmatch(body, -1) {
		if len(m) > 1 && m[1] != "" {
			h.QueryParams = append(h.QueryParams, m[1])
		}
	}

	for _, m := range springRespStatus.FindAllStringSubmatch(body, -1) {
		if m[1] != "" {
			if code, ok := springStatusConstants[m[1]]; ok {
				h.StatusCodes = append(h.StatusCodes, code)
			}
		} else if m[2] != "" {
			if code, ok := parseStatusExpr(m[2]); ok {
				h.StatusCodes = append(h.StatusCodes, code)
			}
		}
	}

	// Response type: prefer Kotlin's explicit `: Type` annotation,
	// otherwise fall back to Java's leading return-type declaration.
	if m := kotlinFunReturnRe.FindStringSubmatch(body); len(m) > 1 {
		h.ResponseType = resolveTypeInFile(unwrapJavaWrappers(m[1]), fileNodes)
	} else if m := springMethodReturnRe.FindStringSubmatch(body); len(m) > 1 {
		rt := unwrapJavaWrappers(m[1])
		if rt != "" && rt != "void" && rt != "Void" && rt != "Unit" {
			h.ResponseType = resolveTypeInFile(rt, fileNodes)
		}
	}

	return h
}

// -----------------------------------------------------------------------------
// JAX-RS provider
//
// JAX-RS method parameters are typed directly. The request body is any
// non-annotated typed parameter; query parameters are @QueryParam,
// path parameters are @PathParam. The return type of the method is
// the response body.
// -----------------------------------------------------------------------------

var (
	jaxrsQueryParam = regexp.MustCompile(`@QueryParam\(\s*"([^"]+)"\s*\)`)
	jaxrsProduces   = regexp.MustCompile(`@Produces\(\s*(?:MediaType\.APPLICATION_(\w+)|"([^"]+)")\s*\)`)
	jaxrsFirstParam = regexp.MustCompile(`\(\s*(?:@Valid\s+)?([A-Za-z_][\w<>.]*)\s+\w+\s*[,)]`)
)

func javaJAXRSDetect(body string, fileNodes []*graph.Node) schemaHints {
	var h schemaHints

	for _, m := range jaxrsQueryParam.FindAllStringSubmatch(body, -1) {
		if len(m) > 1 {
			h.QueryParams = append(h.QueryParams, m[1])
		}
	}
	_ = jaxrsProduces // reserved for content-type hints, not used in this slice.

	// Body: first method parameter whose (annotation-stripped) type
	// isn't a helper. This walks all parameters instead of relying on
	// position so `@QueryParam("x") String x, CreateReq body` still
	// picks out CreateReq.
	for _, p := range parseJavaParamList(body) {
		// p is e.g. "String tenant" or "CreateReq body".
		fields := strings.Fields(p)
		if len(fields) < 2 {
			continue
		}
		typ := stripGenerics(fields[0])
		if typ == "" || javaJAXRSHelperTypes[typ] {
			continue
		}
		// First char upper-case → user type, not a primitive.
		if typ[0] >= 'A' && typ[0] <= 'Z' {
			h.RequestType = resolveTypeInFile(typ, fileNodes)
			break
		}
	}
	_ = jaxrsFirstParam // legacy regex kept for potential reuse.

	// Return type — same pattern as Spring methods.
	if m := springMethodReturnRe.FindStringSubmatch(body); len(m) > 1 {
		rt := unwrapJavaWrappers(m[1])
		if rt != "" && rt != "void" && rt != "Response" {
			h.ResponseType = resolveTypeInFile(rt, fileNodes)
		}
	}

	return h
}

var javaJAXRSHelperTypes = map[string]bool{
	"Request":         true,
	"Response":        true,
	"HttpHeaders":     true,
	"UriInfo":         true,
	"SecurityContext": true,
	"Providers":       true,
	"Application":     true,
	"String":          true,
	"Integer":         true,
	"Long":            true,
	"Double":          true,
	"Boolean":         true,
}

// springStatusConstants maps Spring's HttpStatus enum names onto their
// numeric codes. Spring uses these in `@ResponseStatus(HttpStatus.X)`
// annotations throughout, so every provider detector that surfaces
// a Spring status needs to translate them.
var springStatusConstants = map[string]int{
	"CONTINUE":                       100,
	"SWITCHING_PROTOCOLS":            101,
	"OK":                             200,
	"CREATED":                        201,
	"ACCEPTED":                       202,
	"NO_CONTENT":                     204,
	"MOVED_PERMANENTLY":              301,
	"FOUND":                          302,
	"SEE_OTHER":                      303,
	"NOT_MODIFIED":                   304,
	"TEMPORARY_REDIRECT":             307,
	"PERMANENT_REDIRECT":             308,
	"BAD_REQUEST":                    400,
	"UNAUTHORIZED":                   401,
	"FORBIDDEN":                      403,
	"NOT_FOUND":                      404,
	"METHOD_NOT_ALLOWED":             405,
	"CONFLICT":                       409,
	"GONE":                           410,
	"PRECONDITION_FAILED":            412,
	"UNPROCESSABLE_ENTITY":           422,
	"TOO_MANY_REQUESTS":              429,
	"INTERNAL_SERVER_ERROR":          500,
	"NOT_IMPLEMENTED":                501,
	"BAD_GATEWAY":                    502,
	"SERVICE_UNAVAILABLE":            503,
	"GATEWAY_TIMEOUT":                504,
}

// jaxrsParamAnnRe matches any JAX-RS/Spring param-level annotation.
// We use it to skip annotated params when hunting for the body type —
// `@Valid`, `@QueryParam(...)`, `@PathParam(...)`, `@HeaderParam(...)`,
// etc. `@Valid` *precedes* the body and is allowed through separately
// because the body may carry it.
var jaxrsParamAnnRe = regexp.MustCompile(`@[A-Za-z][\w.]*(?:\([^)]*\))?`)

// methodDeclRe finds a Java/Kotlin method declaration: access
// modifier (optional), return type, name, and opening `(` of its
// parameter list. We use the end of this match as the anchor for
// parameter splitting so annotations like `@Path("/x")` above the
// declaration don't capture their own parens.
var methodDeclRe = regexp.MustCompile(`(?m)(?:^|\s)(?:public|private|protected|fun)\s+(?:static\s+)?(?:final\s+)?[A-Za-z_][\w<>.\s,]*\s+\w+\s*\(`)

// parseJavaParamList splits the argument list of the first method
// declaration found in src into cleaned-up param strings (annotations
// stripped, optional `final` dropped). The result is a list of
// `"Type name"` strings ready to feed into typed-param heuristics.
func parseJavaParamList(src string) []string {
	loc := methodDeclRe.FindStringIndex(src)
	if loc == nil {
		return nil
	}
	// loc[1] is the byte index just after the opening `(`.
	start := loc[1] - 1
	if start < 0 || start >= len(src) || src[start] != '(' {
		return nil
	}
	depth := 0
	end := -1
	for i := start; i < len(src); i++ {
		switch src[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = i
			}
		case '<':
			depth++
		case '>':
			depth--
		}
		if end >= 0 {
			break
		}
	}
	if end <= start+1 {
		return nil
	}
	inner := src[start+1 : end]

	// Split on top-level commas.
	var parts []string
	depth = 0
	partStart := 0
	for i := 0; i < len(inner); i++ {
		switch inner[i] {
		case '(', '<', '[':
			depth++
		case ')', '>', ']':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, strings.TrimSpace(inner[partStart:i]))
				partStart = i + 1
			}
		}
	}
	if last := strings.TrimSpace(inner[partStart:]); last != "" {
		parts = append(parts, last)
	}

	// Strip annotations and `final`.
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = jaxrsParamAnnRe.ReplaceAllString(p, "")
		p = strings.TrimSpace(p)
		p = strings.TrimPrefix(p, "final ")
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// unwrapJavaWrappers peels off common response envelopes so
// `ResponseEntity<UserDto>` / `Mono<UserDto>` / `Flux<UserDto>`
// resolves to `UserDto`.
func unwrapJavaWrappers(t string) string {
	t = strings.TrimSpace(t)
	wrappers := []string{"ResponseEntity", "Mono", "Flux", "CompletableFuture", "Optional", "List", "Set", "Collection", "Iterable"}
	for _, w := range wrappers {
		prefix := w + "<"
		if strings.HasPrefix(t, prefix) && strings.HasSuffix(t, ">") {
			return unwrapJavaWrappers(t[len(prefix) : len(t)-1])
		}
	}
	return stripGenerics(t)
}
