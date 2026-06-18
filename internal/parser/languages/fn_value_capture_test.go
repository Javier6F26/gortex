package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestFnValueCallbackCapture is part of the C3 named set: a function passed by
// bare name as a value (a call argument, an assignment RHS) is captured as a
// callback candidate, while the same function when actually called is not — so
// the resolver gate can later bind only the genuine function-as-value uses.
func TestFnValueCallbackCapture(t *testing.T) {
	src := []byte("package p\n\n" +
		"func handler() {}\n" +
		"func other() {}\n" +
		"func helper() {}\n\n" +
		"func setup() {\n" +
		"\tregister(handler)\n" +
		"\tcb := other\n" +
		"\t_ = cb\n" +
		"\thelper()\n" +
		"}\n")
	res, err := NewGoExtractor().Extract("s.go", src)
	assert.NoError(t, err)

	candidates := map[string]bool{}
	for _, e := range res.Edges {
		if e.Meta == nil {
			continue
		}
		if v, _ := e.Meta["via"].(string); v != "callback_candidate" {
			continue
		}
		if name, _ := e.Meta["fn_value_name"].(string); name != "" {
			candidates[name] = true
			assert.Equal(t, "s.go::setup", e.From, "candidate must be attributed to the enclosing function")
		}
	}
	assert.True(t, candidates["handler"], "handler passed as a call arg should be a callback candidate")
	assert.True(t, candidates["other"], "other assigned to a variable should be a callback candidate")
	assert.False(t, candidates["helper"], "helper() is called, not passed as a value — not a candidate")
}
