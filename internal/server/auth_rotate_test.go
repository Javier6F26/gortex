package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWithAuthFunc_RotatesWithoutRestart asserts the per-request token
// source lets the expected token be added, changed, and removed at
// runtime without rebuilding the handler.
func TestWithAuthFunc_RotatesWithoutRestart(t *testing.T) {
	current := ""
	h := WithAuthFunc(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		func() string { return current },
	)

	call := func(bearer string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	// No token configured -> unauthenticated, any/no bearer passes.
	if got := call(""); got != http.StatusOK {
		t.Fatalf("empty token should allow unauthenticated; got %d", got)
	}

	// Add a token at runtime -> now enforced.
	current = "alpha"
	if got := call(""); got != http.StatusUnauthorized {
		t.Errorf("after adding a token, no-bearer should be 401; got %d", got)
	}
	if got := call("alpha"); got != http.StatusOK {
		t.Errorf("correct token should pass; got %d", got)
	}

	// Rotate the token -> the old one stops working, the new one works.
	current = "beta"
	if got := call("alpha"); got != http.StatusUnauthorized {
		t.Errorf("rotated-away token should be 401; got %d", got)
	}
	if got := call("beta"); got != http.StatusOK {
		t.Errorf("rotated-in token should pass; got %d", got)
	}

	// Remove the token -> unauthenticated again.
	current = ""
	if got := call(""); got != http.StatusOK {
		t.Errorf("after removing the token, unauthenticated should pass; got %d", got)
	}
}
