package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

type fakeOverrideSink struct {
	sets []struct {
		sid, slug string
		enabled   bool
	}
	setErr error
	rows   []RemoteRosterStatus
}

func (f *fakeOverrideSink) SetRemoteOverride(sid, slug string, enabled bool) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.sets = append(f.sets, struct {
		sid, slug string
		enabled   bool
	}{sid, slug, enabled})
	return nil
}

func (f *fakeOverrideSink) ClearRemoteOverride(string, string) error { return nil }

func (f *fakeOverrideSink) RemoteRosterStatus(string) ([]RemoteRosterStatus, error) {
	return f.rows, nil
}

// TestProxyEnable_NoSessionReturnsGuidance asserts the embedded /
// no-daemon-session path returns the "use the global CLI" message rather
// than silently no-op'ing.
func TestProxyEnable_NoSessionReturnsGuidance(t *testing.T) {
	s := newTestServer(t)
	// No sink wired and no session id on the context.
	res, err := s.handleProxyEnable(context.Background(), reqWithArgs(map[string]any{"slug": "r2"}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("proxy_enable without a daemon-backed session must return a guidance error")
	}
}

// TestProxyEnable_WithSinkSetsOverride asserts the toggle reaches the
// sink for a session-bound call.
func TestProxyEnable_WithSinkSetsOverride(t *testing.T) {
	s := newTestServer(t)
	sink := &fakeOverrideSink{}
	s.remoteOverrides = sink
	ctx := WithSessionID(context.Background(), "A")

	res, err := s.handleProxyEnable(ctx, reqWithArgs(map[string]any{"slug": "r2"}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error result: %+v", res.Content)
	}
	if len(sink.sets) != 1 || sink.sets[0].sid != "A" || sink.sets[0].slug != "r2" || !sink.sets[0].enabled {
		t.Fatalf("sink did not receive the enable override: %+v", sink.sets)
	}

	// proxy_disable writes enabled=false.
	if _, err := s.handleProxyDisable(ctx, reqWithArgs(map[string]any{"slug": "r2"})); err != nil {
		t.Fatal(err)
	}
	if len(sink.sets) != 2 || sink.sets[1].enabled {
		t.Fatalf("proxy_disable should record enabled=false: %+v", sink.sets)
	}
}

// TestProxyEnable_UnknownSlugSurfacesError asserts a sink validation
// error becomes a structured tool error.
func TestProxyEnable_UnknownSlugSurfacesError(t *testing.T) {
	s := newTestServer(t)
	s.remoteOverrides = &fakeOverrideSink{setErr: errors.New("unknown remote slug \"ghost\"")}
	ctx := WithSessionID(context.Background(), "A")
	res, err := s.handleProxyEnable(ctx, reqWithArgs(map[string]any{"slug": "ghost"}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("an unknown slug must surface as an error result")
	}
}

// TestProxyStatus_RendersRows asserts proxy_status returns the sink's
// roster rows.
func TestProxyStatus_RendersRows(t *testing.T) {
	s := newTestServer(t)
	on := true
	s.remoteOverrides = &fakeOverrideSink{rows: []RemoteRosterStatus{
		{Slug: "r2", GlobalEnabled: true, Effective: true},
		{Slug: "r3", GlobalEnabled: false, SessionOverride: &on, Effective: true},
	}}
	res, err := s.handleProxyStatus(WithSessionID(context.Background(), "A"), reqWithArgs(nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("proxy_status should succeed: %+v", res.Content)
	}
}

func reqWithArgs(args map[string]any) mcp.CallToolRequest {
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	return req
}
