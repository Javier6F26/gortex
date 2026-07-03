package graph

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAppendCallSite(t *testing.T) {
	e := &Edge{From: "a", To: "b", Kind: EdgeCalls, FilePath: "f.go", Line: 12}

	// The primary site is never duplicated into call_sites.
	AppendCallSite(e, "f.go", 12)
	assert.Empty(t, CallSites(e))

	// Extra sites accumulate, sorted and deduped.
	AppendCallSite(e, "f.go", 20)
	AppendCallSite(e, "f.go", 14)
	AppendCallSite(e, "f.go", 20) // duplicate — ignored
	assert.Equal(t, []string{"f.go:14", "f.go:20"}, CallSites(e))

	// Malformed inputs are ignored.
	AppendCallSite(e, "", 5)
	AppendCallSite(e, "g.go", 0)
	AppendCallSite(nil, "g.go", 5)
	assert.Equal(t, []string{"f.go:14", "f.go:20"}, CallSites(e))
}

func TestCallSites_JSONRoundTripForm(t *testing.T) {
	// A meta round-trip through JSON (disk backend) yields []any, not []string.
	e := &Edge{Meta: map[string]any{"call_sites": []any{"f.go:3", "f.go:9"}}}
	assert.Equal(t, []string{"f.go:3", "f.go:9"}, CallSites(e))
}

func TestSplitCallSite(t *testing.T) {
	f, l := SplitCallSite("dir/f.go:42")
	assert.Equal(t, "dir/f.go", f)
	assert.Equal(t, 42, l)

	for _, bad := range []string{"bad", "f.go:", ":42", "f.go:abc", "f.go:0"} {
		f, l := SplitCallSite(bad)
		assert.Equal(t, "", f, "malformed %q", bad)
		assert.Equal(t, 0, l, "malformed %q", bad)
	}
}
