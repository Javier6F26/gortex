package contracts

import (
	"regexp"
	"strings"

	"github.com/zzet/gortex/internal/graph"
)

// -----------------------------------------------------------------------------
// Dart — dio + package:http consumers
// -----------------------------------------------------------------------------
//
// Flutter apps are almost always the consumer side of HTTP contracts.
// Both dio and package:http accept a payload argument (`data:` or
// `body:`) and return a `Response`/`http.Response` that is then
// deserialised via `fromJson(...)`. We pick up the request type from
// the payload variable and the response type from the `fromJson`
// call's receiver.

func init() {
	schemaEnrichers = append(schemaEnrichers,
		schemaEnricher{
			name:      "dart-consumer",
			languages: []string{"dart"},
			roles:     []Role{RoleConsumer},
			detect:    dartConsumerDetect,
		},
	)
}

var (
	// dio.post(url, data: payload) — also handles keyword arg `data:`
	// appearing anywhere in the call. `[^,)]*` bounds the identifier
	// so we don't swallow an entire literal map.
	dartDioDataRe = regexp.MustCompile(`data\s*:\s*([A-Za-z_$][\w$]*)`)

	// http.post(url, body: jsonEncode(payload)) — common pattern for
	// package:http. Same `body:` kwarg for custom wrappers.
	dartBodyEncodeRe = regexp.MustCompile(`body\s*:\s*jsonEncode\(\s*([A-Za-z_$][\w$]*)\s*\)`)

	// final parsed = FooResp.fromJson(resp.data) — the receiver of
	// .fromJson is the response type.
	dartFromJSONRe = regexp.MustCompile(`([A-Za-z_$][\w$]*)\.fromJson\(`)

	// statusCode: 201 → present in dio request options but uncommon;
	// response.statusCode reads aren't interesting here.
)

func dartConsumerDetect(body string, fileNodes []*graph.Node) schemaHints {
	var h schemaHints

	if m := dartDioDataRe.FindStringSubmatch(body); len(m) > 1 {
		if rt := findDartVarType(body, m[1]); rt != "" {
			h.RequestType = resolveTypeInFile(rt, fileNodes)
		} else if h.RequestExpr == "" {
			h.RequestExpr = "data: " + m[1]
		}
	} else if m := dartBodyEncodeRe.FindStringSubmatch(body); len(m) > 1 {
		if rt := findDartVarType(body, m[1]); rt != "" {
			h.RequestType = resolveTypeInFile(rt, fileNodes)
		} else if h.RequestExpr == "" {
			h.RequestExpr = "body: jsonEncode(" + m[1] + ")"
		}
	}

	if m := dartFromJSONRe.FindStringSubmatch(body); len(m) > 1 {
		h.ResponseType = resolveTypeInFile(m[1], fileNodes)
	}

	return h
}

// findDartVarType mirrors findVarType / findTSVarType for Dart
// declaration forms. Covers:
//
//	final name = Type(...)
//	var   name = Type(...)
//	Type  name = ...
//	(..., Type name, ...)          method parameter
//
// Dart's optional type syntax means many call sites use `final name`
// without a type annotation — we can't resolve those here.
func findDartVarType(body, varName string) string {
	if varName == "" {
		return ""
	}
	v := regexp.QuoteMeta(varName)
	// final|var|const name = Type(
	if m := regexp.MustCompile(`\b(?:final|var|const)\s+` + v + `\s*=\s*([A-Za-z_$][\w$.]*)\(`).FindStringSubmatch(body); len(m) > 1 {
		return m[1]
	}
	// Type name = ...   — leading identifier that looks like a type.
	if m := regexp.MustCompile(`\b([A-Z][\w$]*(?:<[^>]+>)?)\s+` + v + `\b`).FindStringSubmatch(body); len(m) > 1 {
		return strings.TrimSpace(stripGenerics(m[1]))
	}
	return ""
}
