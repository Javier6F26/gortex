package contracts

import (
	"regexp"
	"strings"

	"github.com/zzet/gortex/internal/graph"
)

// -----------------------------------------------------------------------------
// Python — FastAPI + Flask providers, requests / httpx consumers
// -----------------------------------------------------------------------------
//
// FastAPI is the gold case: function params are typed against Pydantic
// models and the return annotation pins the response. Flask is mostly
// untyped, so we fall back to expression capture — `request.get_json()`
// cast expressions, `jsonify(named)` lookups.

func init() {
	schemaEnrichers = append(schemaEnrichers,
		schemaEnricher{
			name:      "py-fastapi-provider",
			languages: []string{"python"},
			roles:     []Role{RoleProvider},
			detect:    pyFastAPIDetect,
		},
		schemaEnricher{
			name:      "py-flask-provider",
			languages: []string{"python"},
			roles:     []Role{RoleProvider},
			detect:    pyFlaskDetect,
		},
		schemaEnricher{
			name:      "py-consumer",
			languages: []string{"python"},
			roles:     []Role{RoleConsumer},
			detect:    pyConsumerDetect,
		},
	)
}

// -----------------------------------------------------------------------------
// FastAPI provider
//
// Handler decoration pattern:
//	@router.post('/users', status_code=201, response_model=UserResp)
//	async def create_user(payload: CreateReq, tenant: str = Query(...)) -> UserResp:
//	    return UserResp(...)
//
// We infer:
//   * request_type from the first param typed as a class that's not
//     Request / Depends / Query / Header / Path (these are FastAPI
//     helpers, not body models)
//   * response_type from `response_model=Foo`, else from the return
//     annotation
//   * status_codes from `status_code=...` and bare `return JSONResponse(..., status_code=NNN)` calls
//   * query_params from params whose default is `Query(...)` or whose
//     name matches a path placeholder
// -----------------------------------------------------------------------------

var (
	pyFastAPIStatusCodeRe   = regexp.MustCompile(`status_code\s*=\s*(?:status\.(\w+)|(\d+))`)
	pyFastAPIResponseModel  = regexp.MustCompile(`response_model\s*=\s*([A-Za-z_][\w.]*)`)
	pyFastAPIReturnAnnot    = regexp.MustCompile(`def\s+\w+\s*\([^)]*\)\s*->\s*([A-Za-z_][\w\[\],\s.]*)\s*:`)
	pyFastAPIFuncSig        = regexp.MustCompile(`def\s+\w+\s*\(([^)]*)\)`)
	pyFastAPIQueryDefaultRe = regexp.MustCompile(`(\w+)\s*:\s*[^=,]+\s*=\s*Query\(`)
)

// helperTypes names FastAPI dependency injection helpers that show up
// in the signature but aren't request-body models. We ignore params
// typed as these when guessing the request type.
var pyFastAPIHelperTypes = map[string]bool{
	"Request":  true,
	"Response": true,
	"Depends":  true,
	"Query":    true,
	"Path":     true,
	"Header":   true,
	"Cookie":   true,
	"Body":     true,
	"Form":     true,
	"File":     true,
	"Security": true,
	"str":      true,
	"int":      true,
	"float":    true,
	"bool":     true,
	"bytes":    true,
	"list":     true,
	"dict":     true,
	"tuple":    true,
	"None":     true,
	"Optional": true,
	"Union":    true,
}

func pyFastAPIDetect(body string, fileNodes []*graph.Node) schemaHints {
	var h schemaHints

	for _, m := range pyFastAPIStatusCodeRe.FindAllStringSubmatch(body, -1) {
		if m[1] != "" {
			if code, ok := pyStatusFromName(m[1]); ok {
				h.StatusCodes = append(h.StatusCodes, code)
			}
		} else if m[2] != "" {
			if code, ok := parseStatusExpr(m[2]); ok {
				h.StatusCodes = append(h.StatusCodes, code)
			}
		}
	}

	if m := pyFastAPIResponseModel.FindStringSubmatch(body); len(m) > 1 {
		h.ResponseType = resolveTypeInFile(m[1], fileNodes)
	}

	if h.ResponseType == "" {
		if m := pyFastAPIReturnAnnot.FindStringSubmatch(body); len(m) > 1 {
			rt := strings.TrimSpace(m[1])
			// Unwrap `List[Foo]`, `Optional[Foo]`, etc.
			if idx := strings.Index(rt, "["); idx >= 0 {
				inner := strings.TrimSuffix(rt[idx+1:], "]")
				// For List[Foo] keep Foo; for Union[Foo, None] take Foo.
				parts := strings.Split(inner, ",")
				rt = strings.TrimSpace(parts[0])
			}
			if rt != "" && rt != "None" {
				h.ResponseType = resolveTypeInFile(rt, fileNodes)
			}
		}
	}

	if m := pyFastAPIFuncSig.FindStringSubmatch(body); len(m) > 1 {
		params := splitPyParams(m[1])
		var queryNames []string
		for _, p := range params {
			name, typ, defaultExpr := parsePyParam(p)
			if name == "" {
				continue
			}
			if strings.Contains(defaultExpr, "Query(") {
				queryNames = append(queryNames, name)
				continue
			}
			// First non-helper class-typed param is the body.
			if h.RequestType == "" && typ != "" && !pyFastAPIHelperTypes[stripGenerics(typ)] {
				first := stripGenerics(typ)
				// Types starting with lower-case are primitives.
				if first != "" && first[0] >= 'A' && first[0] <= 'Z' {
					h.RequestType = resolveTypeInFile(first, fileNodes)
				}
			}
		}
		h.QueryParams = append(h.QueryParams, queryNames...)
	}

	h.QueryParams = append(h.QueryParams, allSubmatches(body, pyFastAPIQueryDefaultRe, 1)...)
	return h
}

// -----------------------------------------------------------------------------
// Flask provider
//
// Flask handlers are untyped; we do what we can:
//	request.get_json() as DictType        → unusual, captured
//	payload: MyReq = request.get_json()   → captured
//	return jsonify(result)                → response (named var)
//	return <expr>, 201                    → status code
// -----------------------------------------------------------------------------

var (
	pyFlaskReqAnn     = regexp.MustCompile(`(\w+)\s*:\s*([A-Za-z_][\w.]*)\s*=\s*request\.get_json\(\)`)
	pyFlaskJSONifyRe  = regexp.MustCompile(`\bjsonify\(\s*([A-Za-z_][\w]*)\s*\)`)
	pyFlaskReturnCode = regexp.MustCompile(`return\s+[^,\n]+?,\s*(\d+)`)
	pyFlaskReqArg     = regexp.MustCompile(`request\.args\.get\(\s*["'](\w+)["']`)
)

func pyFlaskDetect(body string, fileNodes []*graph.Node) schemaHints {
	var h schemaHints
	if m := pyFlaskReqAnn.FindStringSubmatch(body); len(m) > 2 {
		h.RequestType = resolveTypeInFile(m[2], fileNodes)
	}
	if m := pyFlaskJSONifyRe.FindStringSubmatch(body); len(m) > 1 {
		if rt := findPyVarType(body, m[1]); rt != "" {
			h.ResponseType = resolveTypeInFile(rt, fileNodes)
		} else if h.ResponseExpr == "" {
			h.ResponseExpr = "jsonify(" + m[1] + ")"
		}
	}
	for _, m := range pyFlaskReturnCode.FindAllStringSubmatch(body, -1) {
		if code, ok := parseStatusExpr(m[1]); ok {
			h.StatusCodes = append(h.StatusCodes, code)
		}
	}
	h.QueryParams = append(h.QueryParams, allSubmatches(body, pyFlaskReqArg, 1)...)
	return h
}

// -----------------------------------------------------------------------------
// Python consumer — requests / httpx. Captures:
//
//	requests.post(url, json=payload)          → request via payload var
//	httpx.post(url, json=payload)             → same
//	r.json() assigned to a typed var:
//	    data: UserResp = r.json()             → response_type
//	    data = cast(UserResp, r.json())       → response_type
// -----------------------------------------------------------------------------

var (
	pyConsumerJSONRe = regexp.MustCompile(`(?:requests|httpx)\.(?:get|post|put|delete|patch|head|options)\([^)]*?json\s*=\s*([A-Za-z_]\w*)`)
	pyConsumerCastRe = regexp.MustCompile(`cast\(\s*([A-Za-z_][\w.]*)\s*,\s*\w+\.json\(\)`)
	pyConsumerAnnRe  = regexp.MustCompile(`(\w+)\s*:\s*([A-Za-z_][\w.]*)\s*=\s*\w+\.json\(\)`)
)

func pyConsumerDetect(body string, fileNodes []*graph.Node) schemaHints {
	var h schemaHints
	if m := pyConsumerJSONRe.FindStringSubmatch(body); len(m) > 1 {
		if rt := findPyVarType(body, m[1]); rt != "" {
			h.RequestType = resolveTypeInFile(rt, fileNodes)
		} else if h.RequestExpr == "" {
			h.RequestExpr = "json=" + m[1]
		}
	}
	if m := pyConsumerCastRe.FindStringSubmatch(body); len(m) > 1 {
		h.ResponseType = resolveTypeInFile(m[1], fileNodes)
	} else if m := pyConsumerAnnRe.FindStringSubmatch(body); len(m) > 2 {
		h.ResponseType = resolveTypeInFile(m[2], fileNodes)
	}
	return h
}

// -----------------------------------------------------------------------------
// Shared helpers
// -----------------------------------------------------------------------------

// splitPyParams splits a comma-separated function parameter list while
// respecting balanced brackets — `a: Query('x'), b: int = 0`.
func splitPyParams(sig string) []string {
	var out []string
	depth := 0
	start := 0
	for i, ch := range sig {
		switch ch {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ',':
			if depth == 0 {
				p := strings.TrimSpace(sig[start:i])
				if p != "" {
					out = append(out, p)
				}
				start = i + 1
			}
		}
	}
	if last := strings.TrimSpace(sig[start:]); last != "" {
		out = append(out, last)
	}
	return out
}

// parsePyParam breaks `name: Type = default` into its three parts.
// Missing sections return empty strings.
func parsePyParam(p string) (name, typ, def string) {
	// Split off default.
	if idx := strings.Index(p, "="); idx >= 0 {
		def = strings.TrimSpace(p[idx+1:])
		p = strings.TrimSpace(p[:idx])
	}
	if idx := strings.Index(p, ":"); idx >= 0 {
		name = strings.TrimSpace(p[:idx])
		typ = strings.TrimSpace(p[idx+1:])
		return
	}
	name = strings.TrimSpace(p)
	return
}

// findPyVarType scans the body for the first declaration that binds
// `varName` to a type. Supports:
//
//	name: Type = ...             local annotation
//	name: Type                   function parameter annotation
//	name = Type(...)             constructor call
func findPyVarType(body, varName string) string {
	if varName == "" {
		return ""
	}
	v := regexp.QuoteMeta(varName)
	// name: Type = ...  (local annotation with initialiser)
	if m := regexp.MustCompile(`\b` + v + `\s*:\s*([A-Za-z_][\w.]*)\s*=`).FindStringSubmatch(body); len(m) > 1 {
		return m[1]
	}
	// name: Type  followed by , ) =  -  function parameter / bare annotation.
	if m := regexp.MustCompile(`\b` + v + `\s*:\s*([A-Za-z_][\w.]*)(?:\s*[,)]|\s*$)`).FindStringSubmatch(body); len(m) > 1 {
		return m[1]
	}
	// name = Type(...)
	if m := regexp.MustCompile(`\b` + v + `\s*=\s*([A-Za-z_][\w.]*)\(`).FindStringSubmatch(body); len(m) > 1 {
		return m[1]
	}
	return ""
}

// pyStatusFromName maps `starlette.status.HTTP_201_CREATED`-style names
// onto their numeric code. FastAPI uses these constants heavily.
func pyStatusFromName(name string) (int, bool) {
	// Strip HTTP_ prefix and take the first numeric chunk.
	const prefix = "HTTP_"
	if !strings.HasPrefix(name, prefix) {
		return 0, false
	}
	rest := name[len(prefix):]
	digits := ""
	for _, ch := range rest {
		if ch >= '0' && ch <= '9' {
			digits += string(ch)
		} else {
			break
		}
	}
	return parseStatusExpr(digits)
}
