package mcp

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestElideSourceForPattern_ShortStaysVerbatim pins that the elision
// helper passes through short sources untouched — a 5-line example
// must come back byte-identical.
func TestElideSourceForPattern_ShortStaysVerbatim(t *testing.T) {
	src := "line1\nline2\nline3\nline4\nline5"
	got := elideSourceForPattern(src, 40)
	require.Equal(t, src, got)
}

// TestElideSourceForPattern_LongCollapsesWithMarker pins the
// regression that produced multi-KB suggest_pattern responses: a
// large example source must collapse to a head + tail with an
// explicit elision marker the caller can act on.
func TestElideSourceForPattern_LongCollapsesWithMarker(t *testing.T) {
	lines := make([]string, 200)
	for i := range lines {
		lines[i] = "fragment line " + strings.Repeat("x", 50)
	}
	src := strings.Join(lines, "\n")
	got := elideSourceForPattern(src, 30)

	require.NotEqual(t, src, got, "long source must be elided")
	require.Contains(t, got, "lines elided", "elision marker must be present")
	require.Contains(t, got, "max_source_lines", "marker must mention the opt-out")
	// Head + marker + tail is well under the original size.
	require.Less(t, len(got), len(src)/3)
}

// TestElideSourceForPattern_DisableWithZero confirms passing 0 keeps
// the source verbatim — the opt-out path callers reach for when they
// need the full body.
func TestElideSourceForPattern_DisableWithZero(t *testing.T) {
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = "line"
	}
	src := strings.Join(lines, "\n")
	got := elideSourceForPattern(src, 0)
	assert.Equal(t, src, got)
}
