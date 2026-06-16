package savings

import (
	"path/filepath"
	"testing"
	"time"
)

func TestModelClientTotals_InMemory(t *testing.T) {
	s, _ := Open("")
	s.AddObservation(Observation{Tool: "get_symbol_source", Model: "claude-opus-4-8", Client: "claude-code", Returned: 100, Saved: 900})
	s.AddObservation(Observation{Tool: "read_file", Model: "claude-opus-4-8", Client: "claude-code", Returned: 50, Saved: 450})
	s.AddObservation(Observation{Tool: "read_file", Model: "gpt-4.1", Client: "cursor", Returned: 10, Saved: 90})
	s.AddObservation(Observation{Tool: "read_file", Returned: 1, Saved: 5}) // no model / client

	mt, err := s.ModelTotals(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	// Two attributed models; the unattributed call is excluded. Opus
	// (1350 saved) outranks gpt-4.1 (90).
	if len(mt) != 2 {
		t.Fatalf("model totals len = %d, want 2: %+v", len(mt), mt)
	}
	if mt[0].Name != "claude-opus-4-8" || mt[0].TokensSaved != 1350 || mt[0].CallsCounted != 2 {
		t.Errorf("top model row = %+v, want claude-opus-4-8/1350/2", mt[0])
	}

	ct, err := s.ClientTotals(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ct) != 2 {
		t.Fatalf("client totals len = %d, want 2: %+v", len(ct), ct)
	}
	if ct[0].Name != "claude-code" || ct[0].CallsCounted != 2 || ct[0].TokensSaved != 1350 {
		t.Errorf("top client row = %+v, want claude-code/2/1350", ct[0])
	}
}

func TestModelClientTotals_Sidecar(t *testing.T) {
	db := filepath.Join(t.TempDir(), "sidecar.sqlite")
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	s.AddObservation(Observation{Tool: "read_file", Model: "claude-sonnet-4-6", Client: "claude-code", Returned: 20, Saved: 180})
	s.AddObservation(Observation{Tool: "read_file", Model: "claude-sonnet-4-6", Client: "claude-code", Returned: 30, Saved: 270})

	mt, err := s.ModelTotals(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(mt) != 1 || mt[0].Name != "claude-sonnet-4-6" || mt[0].TokensSaved != 450 || mt[0].CallsCounted != 2 {
		t.Fatalf("sidecar model totals = %+v, want one claude-sonnet-4-6/450/2 row", mt)
	}

	// The model / client columns round-trip through the event log.
	evs, err := s.EventsSince(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Model != "claude-sonnet-4-6" || evs[0].Client != "claude-code" {
		t.Fatalf("events did not round-trip model/client: %+v", evs)
	}
}
