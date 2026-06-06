package daemon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestProxyToolCtx_Cancellation asserts a per-remote deadline reaches
// the in-flight HTTP request: a remote that never responds in time is
// abandoned when the ctx deadline elapses, well before the client's
// coarse 60s timeout. This is the bound the federation hot path relies
// on.
func TestProxyToolCtx_Cancellation(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // never responds within the test deadline
	}))
	defer srv.Close()
	defer close(block)

	cli, err := NewServerClient(ServerEntry{Slug: "r2", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, _, err = cli.ProxyToolCtx(ctx, "find_usages", []byte(`{}`))
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected a context error from a never-responding remote")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline-exceeded, got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("cancellation did not reach the wire: took %s (client timeout is 60s)", elapsed)
	}
}

// TestProxyTool_ShimUsesBackground asserts the legacy ProxyTool still
// works (a background-context shim over ProxyToolCtx).
func TestProxyTool_ShimUsesBackground(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	cli, err := NewServerClient(ServerEntry{Slug: "r2", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	out, status, err := cli.ProxyTool("find_usages", []byte(`{}`))
	if err != nil {
		t.Fatalf("ProxyTool: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("want 200, got %d", status)
	}
	if string(out) != `{"ok":true}` {
		t.Fatalf("unexpected body: %s", out)
	}
}
