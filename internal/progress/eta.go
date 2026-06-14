package progress

import (
	"fmt"
	"time"
)

// Phase enumerates the typed sub-phases of an index / enrich run, so
// progress labels are consistent across reporters and surfaces instead of
// ad-hoc strings. They double as human-readable stage labels.
type Phase string

const (
	PhaseDiscover Phase = "discovering files"
	PhaseParse    Phase = "parsing"
	PhaseResolve  Phase = "resolving references"
	PhaseInfer    Phase = "linking symbols"
	PhasePersist  Phase = "persisting"
)

// FormatElapsed renders a duration as a compact human string: "45s",
// "5m 12s", "1h 20m".
func FormatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()+0.5))
	case d < time.Hour:
		m := int(d / time.Minute)
		s := int((d % time.Minute) / time.Second)
		return fmt.Sprintf("%dm %02ds", m, s)
	default:
		h := int(d / time.Hour)
		m := int((d % time.Hour) / time.Minute)
		return fmt.Sprintf("%dh %02dm", h, m)
	}
}

// ETA returns the throughput (items/sec) and estimated time remaining given
// a start time and within-phase counters. A zero/unknown total yields a
// zero eta. Rate is 0 until some time and work have elapsed.
func ETA(start time.Time, current, total int) (itemsPerSec float64, eta time.Duration) {
	elapsed := time.Since(start)
	if elapsed <= 0 || current <= 0 {
		return 0, 0
	}
	itemsPerSec = float64(current) / elapsed.Seconds()
	if total > current && itemsPerSec > 0 {
		remaining := float64(total - current)
		eta = time.Duration(remaining/itemsPerSec) * time.Second
	}
	return itemsPerSec, eta
}

// ProgressStats bundles the derived progress metrics for a phase, ready to
// drop into a readiness / health payload.
type ProgressStats struct {
	Phase       string  `json:"phase"`
	Current     int     `json:"current"`
	Total       int     `json:"total"`
	Percent     int     `json:"percent"`
	ItemsPerSec float64 `json:"items_per_sec"`
	Elapsed     string  `json:"elapsed"`
	ETA         string  `json:"eta,omitempty"`
}

// Stats computes the derived progress metrics for a phase from its start
// time and counters — percent, throughput, human-formatted elapsed, and a
// human-formatted ETA (omitted when the total is unknown).
func Stats(phase string, start time.Time, current, total int) ProgressStats {
	rate, eta := ETA(start, current, total)
	ps := ProgressStats{
		Phase:       phase,
		Current:     current,
		Total:       total,
		ItemsPerSec: rate,
		Elapsed:     FormatElapsed(time.Since(start)),
	}
	if total > 0 {
		ps.Percent = int(float64(current) / float64(total) * 100)
	}
	if eta > 0 {
		ps.ETA = FormatElapsed(eta)
	}
	return ps
}
