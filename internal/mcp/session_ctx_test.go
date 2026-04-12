package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWithSessionID_RoundTrip(t *testing.T) {
	ctx := WithSessionID(context.Background(), "sess_abc")
	assert.Equal(t, "sess_abc", SessionIDFromContext(ctx))
}

func TestWithSessionID_EmptyIsNoOp(t *testing.T) {
	// Empty session IDs shouldn't pollute the ctx tree with a useless
	// key — callers rely on SessionIDFromContext == "" as the signal
	// "no session, use default."
	base := context.Background()
	withEmpty := WithSessionID(base, "")
	assert.Equal(t, base, withEmpty, "empty session ID must return ctx unchanged")
	assert.Equal(t, "", SessionIDFromContext(withEmpty))
}

func TestSessionIDFromContext_NilCtx(t *testing.T) {
	//nolint:staticcheck // intentionally passing nil context to verify defensiveness
	assert.Equal(t, "", SessionIDFromContext(nil))
}

func TestSessionMap_GetCreatesLazily(t *testing.T) {
	m := newSessionMap()
	s1 := m.get("a")
	s2 := m.get("a")
	assert.Same(t, s1, s2, "repeated get(same id) must return the same entry")

	s3 := m.get("b")
	assert.NotSame(t, s1, s3, "different ids must yield distinct entries")
}

func TestSessionMap_Release(t *testing.T) {
	m := newSessionMap()
	original := m.get("a")
	m.release("a")
	reborn := m.get("a")
	assert.NotSame(t, original, reborn,
		"release() must drop the entry so a subsequent get() yields a fresh one")
}

// TestServer_SessionFor_IsolatedState proves that two different session
// IDs get independent per-client activity state — the core guarantee
// that makes the daemon's shared *mcp.Server safe for concurrent
// proxies.
func TestServer_SessionFor_IsolatedState(t *testing.T) {
	srv, _ := setupTestServer(t)

	ctxA := WithSessionID(context.Background(), "session_A")
	ctxB := WithSessionID(context.Background(), "session_B")

	srv.sessionFor(ctxA).recordSymbol("main.go::Foo")
	srv.sessionFor(ctxB).recordSymbol("main.go::Bar")

	snapA := srv.sessionFor(ctxA).snapshot()
	snapB := srv.sessionFor(ctxB).snapshot()

	symsA, _ := snapA["viewed_symbols"].([]string)
	symsB, _ := snapB["viewed_symbols"].([]string)

	assert.Contains(t, symsA, "main.go::Foo", "session A must see its own symbol")
	assert.NotContains(t, symsA, "main.go::Bar", "session A must NOT see session B's symbol")

	assert.Contains(t, symsB, "main.go::Bar", "session B must see its own symbol")
	assert.NotContains(t, symsB, "main.go::Foo", "session B must NOT see session A's symbol")
}

// TestServer_SessionFor_NoIDFallsBackToShared confirms that embedded
// mode (no session ID in ctx) still hits the shared default state, so
// existing single-client behavior is preserved byte-for-byte.
func TestServer_SessionFor_NoIDFallsBackToShared(t *testing.T) {
	srv, _ := setupTestServer(t)

	// No WithSessionID → fallback to shared default.
	sess := srv.sessionFor(context.Background())
	assert.Same(t, srv.session, sess,
		"ctx without session ID must route to the shared default")
}
