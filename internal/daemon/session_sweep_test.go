package daemon

import "testing"

// TestDeadPeerSweepRemovesDeadSession proves the registry reaps sessions whose
// originating client process has died (by handshake PID) while leaving live
// sessions and PID-less (detached/HTTP) sessions untouched.
func TestDeadPeerSweepRemovesDeadSession(t *testing.T) {
	r := NewSessionRegistry()
	r.RegisterDetached("live", Handshake{PID: 111, Mode: ModeMCP})
	r.RegisterDetached("dead", Handshake{PID: 222, Mode: ModeMCP})
	r.RegisterDetached("nopid", Handshake{PID: 0, Mode: ModeMCP})

	// Only PID 111 is alive.
	removed := r.SweepDead(func(pid int) bool { return pid == 111 })

	if len(removed) != 1 || removed[0].ID != "dead" {
		t.Fatalf("SweepDead removed = %+v, want exactly [dead]", removed)
	}
	if r.GetByID("dead") != nil {
		t.Error("a dead-PID session must be removed from the registry")
	}
	if r.GetByID("live") == nil {
		t.Error("a live-PID session must remain")
	}
	if r.GetByID("nopid") == nil {
		t.Error("a session with no recorded PID (0) must NOT be swept — liveness is unknown")
	}
	if r.Count() != 2 {
		t.Errorf("Count() = %d, want 2 after the sweep", r.Count())
	}
}
