package progress

import (
	"testing"
	"time"
)

func TestFormatElapsed(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m 30s"},
		{3700 * time.Second, "1h 01m"},
	}
	for _, c := range cases {
		if got := FormatElapsed(c.d); got != c.want {
			t.Errorf("FormatElapsed(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestETAAndStats(t *testing.T) {
	start := time.Now().Add(-10 * time.Second)

	rate, eta := ETA(start, 100, 200)
	if rate < 5 || rate > 20 { // ~10/s
		t.Errorf("rate = %v, want ~10/s", rate)
	}
	if eta <= 0 {
		t.Error("eta should be positive when total > current")
	}

	// Unknown total → no eta.
	if _, eta2 := ETA(start, 100, 0); eta2 != 0 {
		t.Errorf("unknown total must yield zero eta, got %v", eta2)
	}

	ps := Stats(string(PhaseParse), start, 100, 200)
	if ps.Percent != 50 {
		t.Errorf("percent = %d, want 50", ps.Percent)
	}
	if ps.ETA == "" {
		t.Error("Stats must include an ETA when total known")
	}
	if ps.Phase != "parsing" {
		t.Errorf("phase = %q", ps.Phase)
	}
}
