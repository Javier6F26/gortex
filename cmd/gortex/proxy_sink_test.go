package main

import (
	"testing"

	"github.com/zzet/gortex/internal/daemon"
)

func newSinkFixture(t *testing.T) (*sessionRemoteOverrideSink, *daemon.SessionRegistry, *daemon.Router) {
	t.Helper()
	reg := daemon.NewSessionRegistry()
	cfg := &daemon.ServersConfig{Server: []daemon.ServerEntry{
		{Slug: "r2", URL: "https://r2:4747"},
		{Slug: "r3", URL: "https://r3:4747", Enabled: func() *bool { b := false; return &b }()},
	}}
	router := daemon.NewRouter(daemon.RouterConfig{Servers: cfg, LocalSlug: daemon.LocalServerSentinel})
	sink := &sessionRemoteOverrideSink{sessions: reg, router: func() *daemon.Router { return router }}
	return sink, reg, router
}

// TestSink_SessionIsolation asserts a session override is scoped to its
// own session and reverts to global for another session and after the
// session is torn down (D-30 ephemerality).
func TestSink_SessionIsolation(t *testing.T) {
	sink, reg, router := newSinkFixture(t)
	a := reg.RegisterDetached("A", daemon.Handshake{Mode: daemon.ModeMCP})
	b := reg.RegisterDetached("B", daemon.Handshake{Mode: daemon.ModeMCP})

	// Session A disables r2 (globally on).
	if err := sink.SetRemoteOverride("A", "r2", false); err != nil {
		t.Fatalf("SetRemoteOverride: %v", err)
	}
	enabledSlugs := func(es []daemon.ServerEntry) map[string]bool {
		m := map[string]bool{}
		for _, e := range es {
			m[e.Slug] = true
		}
		return m
	}
	if enabledSlugs(router.EffectiveEnabledRemotes(a))["r2"] {
		t.Error("session A must not see the disabled r2 as enabled")
	}
	if !enabledSlugs(router.EffectiveEnabledRemotes(b))["r2"] {
		t.Error("session B must be unaffected by session A's override")
	}

	// Teardown frees the override (the *Session is dropped).
	reg.RemoveByID("A")
	if err := sink.SetRemoteOverride("A", "r2", false); err == nil {
		t.Error("an override on a torn-down session must error (no session)")
	}
	// A freshly re-registered session under the same id starts clean.
	a2 := reg.RegisterDetached("A", daemon.Handshake{Mode: daemon.ModeMCP})
	if !enabledSlugs(router.EffectiveEnabledRemotes(a2))["r2"] {
		t.Error("a fresh session must inherit the global enabled state, not a stale override")
	}
}

// TestSink_UnknownSlugRejected asserts validation against the live
// roster.
func TestSink_UnknownSlugRejected(t *testing.T) {
	sink, reg, _ := newSinkFixture(t)
	reg.RegisterDetached("A", daemon.Handshake{Mode: daemon.ModeMCP})
	if err := sink.SetRemoteOverride("A", "ghost", true); err == nil {
		t.Fatal("an override for a slug not in the roster must error")
	}
}

// TestSink_RosterStatus asserts the status view reports global + session
// override + effective per remote.
func TestSink_RosterStatus(t *testing.T) {
	sink, reg, _ := newSinkFixture(t)
	reg.RegisterDetached("A", daemon.Handshake{Mode: daemon.ModeMCP})
	if err := sink.SetRemoteOverride("A", "r3", true); err != nil { // enable globally-off r3
		t.Fatal(err)
	}
	rows, err := sink.RemoteRosterStatus("A")
	if err != nil {
		t.Fatal(err)
	}
	byslug := map[string]gortexRosterRow{}
	for _, r := range rows {
		byslug[r.Slug] = gortexRosterRow{r.GlobalEnabled, r.SessionOverride, r.Effective}
	}
	if r := byslug["r2"]; !r.global || r.override != nil || !r.effective {
		t.Errorf("r2 should be global-on, no override, effective-on; got %+v", r)
	}
	if r := byslug["r3"]; r.global || r.override == nil || !*r.override || !r.effective {
		t.Errorf("r3 should be global-off, override-on, effective-on; got %+v", r)
	}
}

type gortexRosterRow struct {
	global    bool
	override  *bool
	effective bool
}
