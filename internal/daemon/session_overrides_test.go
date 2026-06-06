package daemon

import "testing"

// TestSession_RemoteOverride_SetGetClear covers the basic lifecycle of a
// per-session remote override.
func TestSession_RemoteOverride_SetGetClear(t *testing.T) {
	s := &Session{ID: "a"}
	if got := s.RemoteOverrides(); got != nil {
		t.Fatalf("fresh session should have no overrides, got %v", got)
	}
	s.SetRemoteOverride("r2", false)
	s.SetRemoteOverride("r3", true)
	ov := s.RemoteOverrides()
	if ov["r2"] != false || ov["r3"] != true {
		t.Fatalf("overrides not recorded: %v", ov)
	}
	if _, ok := ov["r2"]; !ok {
		t.Fatal("r2 override should be present")
	}
	s.ClearRemoteOverride("r2")
	ov = s.RemoteOverrides()
	if _, ok := ov["r2"]; ok {
		t.Fatal("cleared override should be gone")
	}
	if ov["r3"] != true {
		t.Fatal("clearing r2 must not affect r3")
	}
}

// TestSession_RemoteOverrides_PerSessionIsolation asserts an override on
// one session never leaks into another (R-RMT-2 ephemerality).
func TestSession_RemoteOverrides_PerSessionIsolation(t *testing.T) {
	a := &Session{ID: "a"}
	b := &Session{ID: "b"}
	a.SetRemoteOverride("r2", false)
	if b.RemoteOverrides() != nil {
		t.Fatal("session B must be unaffected by session A's override")
	}
}

// TestSession_RemoteOverrides_CopyOnRead asserts the returned map is a
// copy, so a caller iterating it cannot mutate internal session state.
func TestSession_RemoteOverrides_CopyOnRead(t *testing.T) {
	s := &Session{ID: "a"}
	s.SetRemoteOverride("r2", true)
	ov := s.RemoteOverrides()
	ov["r2"] = false
	ov["injected"] = true
	again := s.RemoteOverrides()
	if again["r2"] != true {
		t.Fatal("mutating the returned copy must not change session state")
	}
	if _, ok := again["injected"]; ok {
		t.Fatal("injected key must not leak into session state")
	}
}

// TestSession_RemoteOverrides_NilSafe asserts the accessors are safe on a
// nil *Session (the embedded / shared server has no per-connection
// session).
func TestSession_RemoteOverrides_NilSafe(t *testing.T) {
	var s *Session
	s.SetRemoteOverride("r2", false) // must not panic
	s.ClearRemoteOverride("r2")
	if s.RemoteOverrides() != nil {
		t.Fatal("nil session should return nil overrides")
	}
}
