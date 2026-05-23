package mcp

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApplyBudget_NoTrimUnderCap verifies the helper is a no-op when
// the marshaled payload already fits — we don't want trimming
// metadata sprayed onto every response, only the oversize ones.
func TestApplyBudget_NoTrimUnderCap(t *testing.T) {
	payload := map[string]any{
		"results": []any{
			map[string]any{"id": "a", "line": 1},
			map[string]any{"id": "b", "line": 2},
		},
		"total": 2,
	}
	out, trimmed := applyBudget(payload, defaultMaxBytes)
	assert.False(t, trimmed)
	assert.Equal(t, payload, out)
}

// TestApplyBudget_TrimsLongestSlice puts a payload that's clearly
// over a tiny budget through the helper and asserts the longest
// list is the one that got cut, with truncation metadata attached.
func TestApplyBudget_TrimsLongestSlice(t *testing.T) {
	rows := make([]any, 200)
	for i := range rows {
		rows[i] = map[string]any{
			"id":   "row-" + strings.Repeat("x", 40),
			"line": i,
			"meta": strings.Repeat("padding-", 10),
		}
	}
	payload := map[string]any{
		"results": rows,
		"others":  []any{map[string]any{"foo": "bar"}}, // shorter list, must NOT be trimmed
		"total":   200,
	}
	out, trimmed := applyBudget(payload, 4_000)
	require.True(t, trimmed, "expected trimming under tight budget")
	m := out.(map[string]any)
	require.Equal(t, true, m[budgetTruncatedKey])
	require.Contains(t, m, "_max_returned_results")
	require.Contains(t, m, "_original_count_results")
	// `others` length stays unchanged — applyBudget only trims the
	// longest list.
	require.Len(t, m["others"], 1)
}

// TestApplyBudget_NoSlicesIsNoOp confirms the helper bails cleanly
// for payloads without a top-level list rather than thrashing — the
// MCP transport's spill-to-disk fallback handles this rare case.
func TestApplyBudget_NoSlicesIsNoOp(t *testing.T) {
	payload := map[string]any{"foo": "bar", "n": 1}
	out, trimmed := applyBudget(payload, 1)
	assert.False(t, trimmed)
	assert.Equal(t, payload, out)
}

// TestApplyFieldsFilter_KeepsOnlyRequested pins the sparse-fieldsets
// projection: list rows are reduced to exactly the keys named in
// `fields`, scalar payload fields are left alone, and unknown keys
// silently drop out (so a typo doesn't turn into an empty payload).
func TestApplyFieldsFilter_KeepsOnlyRequested(t *testing.T) {
	payload := map[string]any{
		"results": []any{
			map[string]any{"id": "a", "name": "A", "doc": "long...", "line": 1},
			map[string]any{"id": "b", "name": "B", "doc": "long...", "line": 2},
		},
		"total": 2,
	}
	out := applyFieldsFilter(payload, []string{"id", "line", "nonexistent"})
	m := out.(map[string]any)
	results := m["results"].([]any)
	require.Len(t, results, 2)
	first := results[0].(map[string]any)
	assert.Contains(t, first, "id")
	assert.Contains(t, first, "line")
	assert.NotContains(t, first, "name")
	assert.NotContains(t, first, "doc")
	assert.NotContains(t, first, "nonexistent")
}

// TestApplyFieldsFilter_EmptyArgIsNoOp confirms the helper returns
// the payload unchanged when no `fields` arg is supplied — every
// existing caller assumes "absent fields = full payload."
func TestApplyFieldsFilter_EmptyArgIsNoOp(t *testing.T) {
	payload := map[string]any{"results": []any{map[string]any{"id": "a"}}}
	out := applyFieldsFilter(payload, nil)
	assert.Equal(t, payload, out)
}

// TestEncodeDecodeCursor_RoundTrip pins the opaque cursor contract:
// callers must pass back exactly what they got, and the offset must
// survive the trip. A malformed cursor decodes to 0 (start) rather
// than failing — defensive against stale cursors after restarts.
func TestEncodeDecodeCursor_RoundTrip(t *testing.T) {
	for _, off := range []int{1, 50, 1000} {
		c := encodeCursor(off)
		assert.Equal(t, off, decodeCursor(c))
	}
	// Empty cursor → offset 0.
	assert.Equal(t, 0, decodeCursor(""))
	// Malformed cursor → 0, no panic.
	assert.Equal(t, 0, decodeCursor("not-a-cursor"))
	// Negative offset rejected.
	assert.Equal(t, 0, decodeCursor(encodeCursor(-5)))
}

// TestTrimGCXBytes_TrimsRowsKeepsHeader pins the GCX byte-trim
// path: the header line is preserved, the tail rows are dropped,
// and a `# truncated_by_budget=true ...` comment records the cut.
// This is the partial-data fallback that replaces the stub-only
// degradation we used to ship — agents on the GCX path now get rows
// and a hint instead of "narrow your query, original was N bytes."
func TestTrimGCXBytes_TrimsRowsKeepsHeader(t *testing.T) {
	header := "GCX1 tool=search_symbols fields=id,kind,name,path,line\n"
	row := "internal/foo.go::Bar\tfunction\tBar\tinternal/foo.go\t10\n"
	var sb strings.Builder
	sb.WriteString(header)
	for i := 0; i < 50; i++ {
		sb.WriteString(row)
	}
	payload := []byte(sb.String())
	cap_ := 1500
	out, trimmed := trimGCXBytes(payload, cap_)
	require.True(t, trimmed)
	require.LessOrEqual(t, len(out), cap_)
	// Header preserved verbatim.
	assert.True(t, strings.HasPrefix(string(out), header), "header must lead the trimmed payload")
	// Truncation comment present with row counts.
	assert.Contains(t, string(out), "# truncated_by_budget=true")
	assert.Contains(t, string(out), "original_rows=50")
}

// TestTrimGCXBytes_NoTrimUnderCap is the fast-path no-op: a payload
// already under the cap must come back byte-identical, with the
// trimmed flag false. We don't want to mutate small payloads or
// append meta rows speculatively.
func TestTrimGCXBytes_NoTrimUnderCap(t *testing.T) {
	payload := []byte("GCX1 tool=t fields=a,b\nfoo\tbar\n")
	out, trimmed := trimGCXBytes(payload, 1024)
	assert.False(t, trimmed)
	assert.Equal(t, string(payload), string(out))
}

// TestTrimGCXBytes_TightCapKeepsOneRow pins the floor: when every
// row crosses the requested cap, the trimmer overshoots by one row
// rather than emitting a zero-row response. A zero-row response
// hides the row shape and forces the caller to guess at how big each
// row is before tightening their scope; keeping one row makes the
// truncation observable.
func TestTrimGCXBytes_TightCapKeepsOneRow(t *testing.T) {
	header := "GCX1 tool=test fields=a,b\n"
	wide := strings.Repeat("x", 200) + "\t" + strings.Repeat("y", 200) + "\n"
	payload := []byte(header + wide + wide + wide)
	// Cap is below header+wide+comment; baseline behaviour would have
	// returned header-only with kept_rows=0.
	out, trimmed := trimGCXBytes(payload, 200)
	require.True(t, trimmed)
	text := string(out)
	require.True(t, strings.HasPrefix(text, header))
	require.Contains(t, text, "# truncated_by_budget=true")
	require.Contains(t, text, "original_rows=3")
	require.Contains(t, text, "kept_rows=1")
	// One row's worth of overshoot is the bound; the response stays
	// dominated by one row + bookkeeping.
	require.Less(t, len(out), len(header)+len(wide)+200)
}

// TestEffectiveBudget_DefaultAndOptOut verifies the budget-by-default
// contract: callers who don't specify get the project default, an
// explicit `max_bytes` overrides, and `max_bytes: 0` is the explicit
// opt-out (rare — for tasks needing exhaustive enumeration). The
// flip from opt-in to opt-out is deliberate: tools that spill teach
// agents to deprioritise them, so the default has to deliver an
// inline answer with truncation metadata instead.
func TestEffectiveBudget_DefaultAndOptOut(t *testing.T) {
	// No opt-out → project default applies.
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{}
	assert.Equal(t, defaultMaxBytes, effectiveBudget(req))

	// Explicit max_bytes wins.
	req.Params.Arguments = map[string]any{"max_bytes": float64(20000)}
	assert.Equal(t, 20000, effectiveBudget(req))

	// max_bytes: 0 is the explicit opt-out — caller asks for no cap.
	req.Params.Arguments = map[string]any{"max_bytes": float64(0)}
	assert.Equal(t, 0, effectiveBudget(req))

	// Negative max_bytes also opts out (defensive against int / float
	// coercion bugs in the caller).
	req.Params.Arguments = map[string]any{"max_bytes": float64(-1)}
	assert.Equal(t, 0, effectiveBudget(req))

	// int-typed max_bytes (some clients pass ints, not floats).
	req.Params.Arguments = map[string]any{"max_bytes": 15000}
	assert.Equal(t, 15000, effectiveBudget(req))
}

// TestApplyDegradation_StripsBeforeDropping pins the cascade order:
// when a payload exceeds the budget, the helper first strips the
// MetaStrip keys, only dropping rows if stripping alone wasn't
// enough. This is the cheapest signal to drop — a `doc` column can
// often save 50% of the payload without losing a single row.
func TestApplyDegradation_StripsBeforeDropping(t *testing.T) {
	rows := make([]any, 20)
	for i := range rows {
		rows[i] = map[string]any{
			"id":   "row-" + strings.Repeat("x", 5),
			"kind": "function",
			"doc":  strings.Repeat("padding-", 100), // big strippable column
		}
	}
	payload := map[string]any{"results": rows}
	shape := DegradeShape{
		MetaStrip: []string{"doc"},
		TierFunc: func(r map[string]any) int {
			return 1 // every row is keep-tier so any drop must come from elsewhere
		},
	}
	out, trimmed := applyDegradation(payload, shape, 2_000)
	require.True(t, trimmed)
	m := out.(map[string]any)
	results := m["results"].([]any)
	// All rows kept (none would have been dropped — every row is tier 1).
	require.Len(t, results, 20)
	// `doc` removed; `id`/`kind` survive.
	first := results[0].(map[string]any)
	assert.NotContains(t, first, "doc")
	assert.Contains(t, first, "id")
	assert.Contains(t, m, "_meta_stripped")
}

// TestApplyDegradation_DropsLowTierFirst pins the priority order:
// tier-3 rows are dropped before tier-2 ones. A payload mixing
// "function" (tier 1) and "param" (tier 3) rows under tight budget
// must keep all functions and drop all params, not the other way
// round.
func TestApplyDegradation_DropsLowTierFirst(t *testing.T) {
	var rows []any
	for i := 0; i < 30; i++ {
		rows = append(rows, map[string]any{
			"id":   "func-" + strings.Repeat("x", 50),
			"kind": "function",
		})
	}
	for i := 0; i < 200; i++ {
		rows = append(rows, map[string]any{
			"id":   "param-" + strings.Repeat("x", 50),
			"kind": "param",
		})
	}
	payload := map[string]any{"results": rows}
	out, trimmed := applyDegradation(payload, DegradeShape{TierFunc: symbolKindTier}, 4_000)
	require.True(t, trimmed)
	m := out.(map[string]any)
	results := m["results"].([]any)
	for _, row := range results {
		rm := row.(map[string]any)
		// No `param` row should have survived — they're tier 3 and
		// got dropped first under the cascade.
		assert.NotEqual(t, "param", rm["kind"])
	}
}

// TestApplyOffsetLimit_WindowAndCursor verifies the offset/limit
// helper used by paginating handlers: the window is correct, the
// next_cursor is empty when the window covers the tail, and
// out-of-range offsets degrade to an empty page.
func TestApplyOffsetLimit_WindowAndCursor(t *testing.T) {
	rows := make([]any, 10)
	for i := range rows {
		rows[i] = i
	}

	// Page 1.
	page, next := applyOffsetLimit(rows, 0, 4)
	require.Len(t, page, 4)
	assert.Equal(t, 4, decodeCursor(next))

	// Page 2.
	page, next = applyOffsetLimit(rows, 4, 4)
	require.Len(t, page, 4)
	assert.Equal(t, 8, decodeCursor(next))

	// Last partial page — no next_cursor.
	page, next = applyOffsetLimit(rows, 8, 4)
	require.Len(t, page, 2)
	assert.Empty(t, next)

	// Offset past end → empty page, no cursor.
	page, next = applyOffsetLimit(rows, 100, 4)
	assert.Len(t, page, 0)
	assert.Empty(t, next)
}

// TestTokensToBytes_RoundTrip pins the token→byte→token round-trip
// inside ±1 of the calibration ratio. Token estimation is inherently
// lossy across tokenisers; this test guards the conversion math
// itself, not the model-specific accuracy.
func TestTokensToBytes_RoundTrip(t *testing.T) {
	for _, tokens := range []int{1, 50, 1000, 10000} {
		bytes := tokensToBytes(tokens)
		assert.Equal(t, int(float64(tokens)*avgBytesPerToken), bytes)
		// Reverse direction should land within ±1 because of int cast.
		recovered := bytesToTokens(bytes)
		assert.InDelta(t, tokens, recovered, 1, "round-trip drifted for tokens=%d", tokens)
	}
	// Non-positive inputs collapse to 0 (opt-out semantics).
	assert.Equal(t, 0, tokensToBytes(0))
	assert.Equal(t, 0, tokensToBytes(-1))
	assert.Equal(t, 0, bytesToTokens(0))
	assert.Equal(t, 0, bytesToTokens(-1))
}

// TestEffectiveBudget_MaxTokensAlone covers the new token-only path:
// passing max_tokens with no max_bytes derives a byte cap via the
// avgBytesPerToken ratio. Opt-out (0 / negative) on tokens-only also
// returns 0 cap, matching the bytes-only semantics.
func TestEffectiveBudget_MaxTokensAlone(t *testing.T) {
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"max_tokens": float64(1000)}
	assert.Equal(t, tokensToBytes(1000), effectiveBudget(req))

	// int-typed token arg.
	req.Params.Arguments = map[string]any{"max_tokens": 2000}
	assert.Equal(t, tokensToBytes(2000), effectiveBudget(req))

	// Opt-out.
	req.Params.Arguments = map[string]any{"max_tokens": float64(0)}
	assert.Equal(t, 0, effectiveBudget(req))

	req.Params.Arguments = map[string]any{"max_tokens": float64(-1)}
	assert.Equal(t, 0, effectiveBudget(req))
}

// TestEffectiveBudget_BothAxesTighterWins is the central
// composition rule: when both max_bytes and max_tokens are
// supplied with positive values, the cap is the smaller of the two.
// This is the "tighter wins" contract documented in effectiveBudget.
func TestEffectiveBudget_BothAxesTighterWins(t *testing.T) {
	req := mcp.CallToolRequest{}

	// max_tokens implies fewer bytes (tokens × 3.5) — tokens drives.
	req.Params.Arguments = map[string]any{
		"max_bytes":  float64(100_000),
		"max_tokens": float64(100), // ~350 bytes
	}
	assert.Equal(t, tokensToBytes(100), effectiveBudget(req))

	// max_bytes is tighter — bytes drives.
	req.Params.Arguments = map[string]any{
		"max_bytes":  float64(500),
		"max_tokens": float64(10000), // ~35000 bytes
	}
	assert.Equal(t, 500, effectiveBudget(req))

	// Equal numeric values, tokens still expands via ratio so bytes
	// will be tighter.
	req.Params.Arguments = map[string]any{
		"max_bytes":  float64(1000),
		"max_tokens": float64(1000), // ~3500 bytes
	}
	assert.Equal(t, 1000, effectiveBudget(req))
}

// TestEffectiveBudget_PerAxisOptOut verifies per-axis opt-out: when
// one axis is 0 (opt-out) and the other is positive, the positive
// one still applies. Only when BOTH are opt-out does the request
// fall through to "no cap."
func TestEffectiveBudget_PerAxisOptOut(t *testing.T) {
	req := mcp.CallToolRequest{}

	// bytes opt-out, tokens positive → tokens drives.
	req.Params.Arguments = map[string]any{
		"max_bytes":  float64(0),
		"max_tokens": float64(500),
	}
	assert.Equal(t, tokensToBytes(500), effectiveBudget(req))

	// tokens opt-out, bytes positive → bytes drives.
	req.Params.Arguments = map[string]any{
		"max_bytes":  float64(2000),
		"max_tokens": float64(0),
	}
	assert.Equal(t, 2000, effectiveBudget(req))

	// Both opt-out → no cap.
	req.Params.Arguments = map[string]any{
		"max_bytes":  float64(0),
		"max_tokens": float64(0),
	}
	assert.Equal(t, 0, effectiveBudget(req))
}

// TestResolveBudgetSource_AttributesTighterAxis confirms which axis
// gets credit for the cap — used by decorateTokenBudgetJSON /
// decorateTokenBudgetGCX to render the correct truncation hint.
func TestResolveBudgetSource_AttributesTighterAxis(t *testing.T) {
	req := mcp.CallToolRequest{}

	// No args → default.
	req.Params.Arguments = map[string]any{}
	src, _ := resolveBudgetSource(req)
	assert.Equal(t, budgetSourceDefault, src)

	// Tokens alone.
	req.Params.Arguments = map[string]any{"max_tokens": float64(500)}
	src, val := resolveBudgetSource(req)
	assert.Equal(t, budgetSourceTokens, src)
	assert.Equal(t, 500, val)

	// Bytes alone.
	req.Params.Arguments = map[string]any{"max_bytes": float64(5000)}
	src, val = resolveBudgetSource(req)
	assert.Equal(t, budgetSourceBytes, src)
	assert.Equal(t, 5000, val)

	// Both — tokens is tighter (100 tokens ≈ 350 bytes < 5000 bytes).
	req.Params.Arguments = map[string]any{
		"max_bytes":  float64(5000),
		"max_tokens": float64(100),
	}
	src, val = resolveBudgetSource(req)
	assert.Equal(t, budgetSourceTokens, src)
	assert.Equal(t, 100, val)

	// Both — bytes is tighter.
	req.Params.Arguments = map[string]any{
		"max_bytes":  float64(500),
		"max_tokens": float64(10000),
	}
	src, val = resolveBudgetSource(req)
	assert.Equal(t, budgetSourceBytes, src)
	assert.Equal(t, 500, val)

	// Per-axis opt-out: bytes 0, tokens positive → tokens wins.
	req.Params.Arguments = map[string]any{
		"max_bytes":  float64(0),
		"max_tokens": float64(500),
	}
	src, val = resolveBudgetSource(req)
	assert.Equal(t, budgetSourceTokens, src)
	assert.Equal(t, 500, val)

	// Both opt-out.
	req.Params.Arguments = map[string]any{
		"max_bytes":  float64(0),
		"max_tokens": float64(0),
	}
	src, _ = resolveBudgetSource(req)
	assert.Equal(t, budgetSourceNone, src)
}

// TestDecorateTokenBudgetJSON_AddsMarkerWhenTokensDriven pins the
// decoration contract: when max_tokens drove the budget AND the
// trim guard fired, the resulting payload carries _max_tokens and
// _truncated_by_tokens markers alongside the existing
// _truncated_by_budget flag.
func TestDecorateTokenBudgetJSON_AddsMarkerWhenTokensDriven(t *testing.T) {
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"max_tokens": float64(100)}

	// Simulate a trimmed payload — applyBudget's contract is that
	// `_truncated_by_budget=true` rides on every trim.
	trimmed := map[string]any{
		"results":                 []any{},
		budgetTruncatedKey:        true,
		"_max_returned_results":   0,
		"_original_count_results": 50,
	}
	out := decorateTokenBudgetJSON(trimmed, req).(map[string]any)
	assert.Equal(t, 100, out["_max_tokens"])
	assert.Equal(t, true, out["_truncated_by_tokens"])
}

// TestDecorateTokenBudgetJSON_NoMarkerWhenBytesDriven verifies the
// decoration only fires for tokens-driven trims. When max_bytes is
// the tighter axis, the response should not falsely claim "tokens
// caused this" — but it WILL still surface the user's max_tokens
// value (without `_truncated_by_tokens`) so the agent sees how its
// token budget compared.
func TestDecorateTokenBudgetJSON_NoMarkerWhenBytesDriven(t *testing.T) {
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"max_bytes":  float64(500),
		"max_tokens": float64(10000), // ~35000 bytes — bytes is tighter
	}

	trimmed := map[string]any{
		"results":                 []any{},
		budgetTruncatedKey:        true,
		"_max_returned_results":   0,
		"_original_count_results": 100,
	}
	out := decorateTokenBudgetJSON(trimmed, req).(map[string]any)
	// Bytes drove the trim, so no _truncated_by_tokens.
	assert.NotContains(t, out, "_truncated_by_tokens")
	// But the requested max_tokens still rides for caller visibility.
	assert.Equal(t, 10000, out["_max_tokens"])
}

// TestDecorateTokenBudgetJSON_NoOpWhenNotTrimmed: a payload without
// the truncation flag must not gain token-budget metadata. We must
// never decorate untrimmed responses — that would spam every
// response with budget meta.
func TestDecorateTokenBudgetJSON_NoOpWhenNotTrimmed(t *testing.T) {
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"max_tokens": float64(100)}

	untrimmed := map[string]any{
		"results": []any{map[string]any{"id": "a"}},
		"total":   1,
	}
	out := decorateTokenBudgetJSON(untrimmed, req).(map[string]any)
	assert.NotContains(t, out, "_max_tokens")
	assert.NotContains(t, out, "_truncated_by_tokens")
}

// TestDecorateTokenBudgetJSON_NoOpWhenNoTokensArg: even on a trimmed
// payload, no decoration happens unless the caller actually passed
// max_tokens. Otherwise we'd inject a misleading marker.
func TestDecorateTokenBudgetJSON_NoOpWhenNoTokensArg(t *testing.T) {
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"max_bytes": float64(500)}

	trimmed := map[string]any{
		"results":                 []any{},
		budgetTruncatedKey:        true,
		"_max_returned_results":   0,
		"_original_count_results": 100,
	}
	out := decorateTokenBudgetJSON(trimmed, req).(map[string]any)
	assert.NotContains(t, out, "_max_tokens")
	assert.NotContains(t, out, "_truncated_by_tokens")
}

// TestDecorateTokenBudgetGCX_AddsCommentWhenTrimmed exercises the
// GCX decoration path: a payload carrying the trim comment from
// trimGCXBytes gets a sibling `# max_tokens=N truncated_by_tokens=true`
// line appended. Decoders treat the second comment as a no-op.
func TestDecorateTokenBudgetGCX_AddsCommentWhenTrimmed(t *testing.T) {
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"max_tokens": float64(250)}

	payload := []byte("GCX1 tool=test fields=a\nrow\n# truncated_by_budget=true original_rows=10 kept_rows=2\n")
	out := decorateTokenBudgetGCX(payload, req)

	assert.Contains(t, string(out), "max_tokens=250")
	assert.Contains(t, string(out), "truncated_by_tokens=true")
}

// TestDecorateTokenBudgetGCX_NoOpWithoutTrimComment confirms we only
// decorate when the GCX trim path already fired — without the
// canonical `# truncated_by_budget=true` line, we leave the payload
// alone (caller didn't actually exceed the budget).
func TestDecorateTokenBudgetGCX_NoOpWithoutTrimComment(t *testing.T) {
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"max_tokens": float64(250)}

	payload := []byte("GCX1 tool=test fields=a\nrow\n")
	out := decorateTokenBudgetGCX(payload, req)
	assert.Equal(t, string(payload), string(out))
}

// TestDecorateTokenBudgetGCX_NoOpWithoutTokensArg: no max_tokens →
// no decoration, even on a trimmed payload.
func TestDecorateTokenBudgetGCX_NoOpWithoutTokensArg(t *testing.T) {
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"max_bytes": float64(500)}

	payload := []byte("GCX1 tool=test fields=a\nrow\n# truncated_by_budget=true original_rows=10 kept_rows=2\n")
	out := decorateTokenBudgetGCX(payload, req)
	assert.Equal(t, string(payload), string(out))
}

// TestDecorateTokenBudgetGCX_IdempotentDecoration guards against the
// rare case where a payload passes through the decorator twice
// (e.g. a retry path) — the comment should only appear once so
// the response stays well-formed.
func TestDecorateTokenBudgetGCX_IdempotentDecoration(t *testing.T) {
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"max_tokens": float64(250)}

	payload := []byte("GCX1 tool=test fields=a\nrow\n# truncated_by_budget=true original_rows=10 kept_rows=2\n")
	once := decorateTokenBudgetGCX(payload, req)
	twice := decorateTokenBudgetGCX(once, req)
	assert.Equal(t, string(once), string(twice), "decorator must be idempotent")
	// Exactly one max_tokens comment.
	assert.Equal(t, 1, strings.Count(string(twice), "max_tokens=250"))
}
