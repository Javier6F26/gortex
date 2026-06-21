package main

import "testing"

// TestPeekFrameToolName covers the helper that drives promote-on-demand: it
// must return the tool name for a tools/call frame and "" for anything else,
// so the dispatcher only promotes on a genuine tool call.
func TestPeekFrameToolName(t *testing.T) {
	cases := []struct {
		name  string
		frame string
		want  string
	}{
		{"tools/call", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"flow_between","arguments":{"source_id":"a"}}}`, "flow_between"},
		{"tools/call no args", `{"method":"tools/call","params":{"name":"feedback"}}`, "feedback"},
		{"tools/list is not a call", `{"method":"tools/list","params":{}}`, ""},
		{"initialize is not a call", `{"method":"initialize","params":{"name":"x"}}`, ""},
		{"missing name", `{"method":"tools/call","params":{}}`, ""},
		{"malformed json", `{not json`, ""},
		{"empty", ``, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := peekFrameToolName([]byte(c.frame)); got != c.want {
				t.Fatalf("peekFrameToolName(%s) = %q, want %q", c.frame, got, c.want)
			}
		})
	}
}
