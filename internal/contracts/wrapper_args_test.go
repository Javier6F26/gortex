package contracts

import (
	"testing"
)

// TestExtractFirstCallArg covers the call-site-parsing contract every
// other piece of wrapper inlining relies on. If this goes wrong then
// InlineWrappers silently skips real callers or emits nonsense
// contracts — both failure modes are invisible in end-to-end tests
// until they corrupt match counts, so it's worth exhaustively
// specifying the allowed inputs here.
func TestExtractFirstCallArg(t *testing.T) {
	tests := []struct {
		name       string
		src        string
		line       int
		wrapper    string
		lang       string
		wantKind   argKind
		wantValue  string
		wantMethod string
	}{
		{
			name:      "string literal",
			src:       "function f() {\n  return request('/v1/users', getToken);\n}\n",
			line:      2,
			wrapper:   "request",
			lang:      "typescript",
			wantKind:  argLiteral,
			wantValue: "/v1/users",
		},
		{
			name:      "template literal with param",
			src:       "function f() {\n  return request(`/v1/users/${id}`, getToken);\n}\n",
			line:      2,
			wrapper:   "request",
			lang:      "typescript",
			wantKind:  argLiteral,
			wantValue: "/v1/users/${id}",
		},
		{
			name:       "string literal + object method",
			src:        "function f() {\n  return request('/v1/tucks', getToken, {method: 'POST', body: JSON.stringify(d)});\n}\n",
			line:       2,
			wrapper:    "request",
			lang:       "typescript",
			wantKind:   argLiteral,
			wantValue:  "/v1/tucks",
			wantMethod: "POST",
		},
		{
			name:       "double-quoted method with backticks in wrapper",
			src:        "function f() {\n  return request(`/v1/tucks/${id}`, getToken, { method: \"PATCH\" });\n}\n",
			line:       2,
			wrapper:    "request",
			lang:       "typescript",
			wantKind:   argLiteral,
			wantValue:  "/v1/tucks/${id}",
			wantMethod: "PATCH",
		},
		{
			name:     "bare parameter identifier",
			src:      "function doFetch(path, token) {\n  return fetch(path, opts);\n}\n",
			line:     2,
			wrapper:  "fetch",
			lang:     "javascript",
			wantKind: argBareParam,
		},
		{
			name:     "pure interpolation template — wrapper forwarding",
			src:      "function f(url) {\n  return request(`${url}`, getToken);\n}\n",
			line:     2,
			wrapper:  "request",
			lang:     "typescript",
			wantKind: argBareParam,
		},
		{
			name:      "generic type params on TS wrapper",
			src:       "function f() {\n  return request<User>('/v1/me', getToken);\n}\n",
			line:      2,
			wrapper:   "request",
			lang:      "typescript",
			wantKind:  argLiteral,
			wantValue: "/v1/me",
		},
		{
			name:     "runtime expression — not a literal, not a bare ident",
			src:      "function f() {\n  return request(buildPath() + '/v1/x', getToken);\n}\n",
			line:     2,
			wrapper:  "request",
			lang:     "typescript",
			wantKind: argUnknown,
		},
		{
			name:       "go-ish call with nested map options",
			src:        "func f() {\n\treturn request(\"/v1/tucks\", token, map[string]any{\"method\": \"DELETE\"})\n}\n",
			line:       2,
			wrapper:    "request",
			lang:       "go",
			wantKind:   argLiteral,
			wantValue:  "/v1/tucks",
			wantMethod: "DELETE",
		},
		{
			name:      "multi-line call spanning across object-literal body",
			src:       "function f() {\n  return request('/v1/tucks', getToken, {\n    method: 'POST',\n    body: JSON.stringify(data),\n  });\n}\n",
			line:      2,
			wrapper:   "request",
			lang:      "typescript",
			wantKind:  argLiteral,
			wantValue: "/v1/tucks",
			wantMethod: "POST",
		},
		{
			name:     "wrapper name not on the target line",
			src:      "function f() {\n  const x = 1;\n  return request('/v1/users', getToken);\n}\n",
			line:     2,
			wrapper:  "request",
			lang:     "typescript",
			wantKind: argUnknown,
		},
		{
			name:     "different function name on the line",
			src:      "function f() {\n  return callApi('/v1/users', getToken);\n}\n",
			line:     2,
			wrapper:  "request",
			lang:     "typescript",
			wantKind: argUnknown,
		},
		{
			name:     "word-boundary — xrequest should not match request",
			src:      "function f() {\n  return xrequest('/v1/users', getToken);\n}\n",
			line:     2,
			wrapper:  "request",
			lang:     "typescript",
			wantKind: argUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFirstCallArg([]byte(tt.src), tt.line, tt.wrapper, tt.lang)
			if got.Kind != tt.wantKind {
				t.Errorf("Kind: want %v, got %v", tt.wantKind, got.Kind)
			}
			if tt.wantKind == argLiteral && got.Value != tt.wantValue {
				t.Errorf("Value: want %q, got %q", tt.wantValue, got.Value)
			}
			if got.Method != tt.wantMethod {
				t.Errorf("Method: want %q, got %q", tt.wantMethod, got.Method)
			}
		})
	}
}

func TestIsWrapperPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/{path}", true},
		{"/{url}", true},
		{"/{endpoint}", true},
		{"/path", true},
		{"/url", true},
		{"/v1/tucks", false},
		{"/v1/tucks/{id}", false},
		{"/", false},
		{"", false},
		{"/{id}/suffix", false},
	}
	for _, tt := range tests {
		if got := isWrapperPath(tt.path); got != tt.want {
			t.Errorf("isWrapperPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}
