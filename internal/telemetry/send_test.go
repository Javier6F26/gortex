package telemetry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func fixedClock(day string) func() time.Time {
	t, _ := time.Parse("2006-01-02", day)
	return func() time.Time { return t }
}

func TestInstallIDStableAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	a := InstallID(dir)
	b := InstallID(dir)
	if a == "" {
		t.Fatal("InstallID returned empty")
	}
	if a != b {
		t.Errorf("InstallID not stable: %q != %q", a, b)
	}
	// A fresh dir mints a different id.
	if c := InstallID(t.TempDir()); c == a {
		t.Errorf("two machines share an install id: %q", c)
	}
}

func seedCompletedDay(t *testing.T, store *Store, day string) {
	t.Helper()
	r := &Rollup{Day: day, Counts: map[string]int{}}
	r.Add("mcp_tool_call", "search_symbols")
	if err := store.Save(r); err != nil {
		t.Fatalf("seed %s: %v", day, err)
	}
}

func TestMaybeSendTransmitsAndClearsCompletedDays(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	seedCompletedDay(t, store, "2026-06-16")
	seedCompletedDay(t, store, "2026-06-17")
	// Today's open day must NOT be sent.
	open := &Rollup{Day: "2026-06-18", Counts: map[string]int{}}
	open.Add("cli_command", "review")
	_ = store.Save(open)

	var got Payload
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	s := NewSender(store, dir, "v9.9.9", func(k string) string {
		if k == EnvEndpoint {
			return srv.URL
		}
		return ""
	})
	s.now = fixedClock("2026-06-18")

	s.MaybeSend(context.Background(), Consent{Enabled: true})

	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("endpoint hit %d times, want 1", hits)
	}
	if got.SchemaVersion != sendSchemaVersion || got.GortexVersion != "v9.9.9" || got.InstallID == "" {
		t.Errorf("payload envelope wrong: %+v", got)
	}
	if len(got.Days) != 2 {
		t.Fatalf("payload carried %d days, want 2 (open day excluded)", len(got.Days))
	}
	// Sent days deleted; the open day survives.
	days, _ := store.Days()
	if len(days) != 1 || days[0] != "2026-06-18" {
		t.Errorf("after send remaining days = %v, want [2026-06-18]", days)
	}
}

func TestMaybeSendNoEndpointIsBlocked(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	seedCompletedDay(t, store, "2026-06-17")

	// No EnvEndpoint set → the live send is blocked.
	s := NewSender(store, dir, "v1", func(string) string { return "" })
	s.now = fixedClock("2026-06-18")
	s.MaybeSend(context.Background(), Consent{Enabled: true})

	// Nothing transmitted, nothing deleted, no last-send marker.
	if days, _ := store.Days(); len(days) != 1 {
		t.Errorf("blocked send deleted buffered days: %v", days)
	}
	if s.lastSendDay() != "" {
		t.Error("blocked send wrote a last-send marker")
	}
}

func TestMaybeSendConsentDisabled(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	seedCompletedDay(t, store, "2026-06-17")
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	s := NewSender(store, dir, "v1", func(k string) string {
		if k == EnvEndpoint {
			return srv.URL
		}
		return ""
	})
	s.now = fixedClock("2026-06-18")
	s.MaybeSend(context.Background(), Consent{Enabled: false})

	if atomic.LoadInt32(&hits) != 0 {
		t.Errorf("disabled consent still transmitted (%d hits)", hits)
	}
}

func TestMaybeSendOncePerDay(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	seedCompletedDay(t, store, "2026-06-17")
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	mk := func() *Sender {
		s := NewSender(store, dir, "v1", func(k string) string {
			if k == EnvEndpoint {
				return srv.URL
			}
			return ""
		})
		s.now = fixedClock("2026-06-18")
		return s
	}
	mk().MaybeSend(context.Background(), Consent{Enabled: true})
	// Re-seed a completed day and try again the same UTC day: must not re-send.
	seedCompletedDay(t, store, "2026-06-16")
	mk().MaybeSend(context.Background(), Consent{Enabled: true})

	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("sent %d times in one day, want 1", hits)
	}
}

func TestMaybeSendServerErrorKeepsDays(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	seedCompletedDay(t, store, "2026-06-17")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := NewSender(store, dir, "v1", func(k string) string {
		if k == EnvEndpoint {
			return srv.URL
		}
		return ""
	})
	s.now = fixedClock("2026-06-18")
	s.MaybeSend(context.Background(), Consent{Enabled: true}) // must not panic

	// A 5xx is final (no retry) but the days are NOT deleted — they ship later.
	if days, _ := store.Days(); len(days) != 1 {
		t.Errorf("server error dropped buffered days: %v", days)
	}
}
