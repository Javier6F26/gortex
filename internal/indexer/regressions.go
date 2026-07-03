package indexer

import "sync/atomic"

// resolutionRegressions counts how many times a shape-degradation guard has
// fired since process start: a live per-file patch or a warm reload that lost
// a material share of a unit's resolved edges with no matching symbol removal.
// It is the tamper-evidence signal a ratchet can no longer hide behind — the
// guards self-heal (persist-side re-resolve; boot-side re-index) AND bump this
// counter, so index_health surfaces that the daemon caught a regression rather
// than silently serving a shrunken graph. Process-global because the two guards
// live in different packages (indexer watcher, cmd/gortex boot) and the reader
// (index_health) in a third.
var resolutionRegressions atomic.Int64

// RecordResolutionRegression bumps the process-global regression counter.
func RecordResolutionRegression() { resolutionRegressions.Add(1) }

// ResolutionRegressions returns the number of shape-degradation guard firings
// since process start.
func ResolutionRegressions() int64 { return resolutionRegressions.Load() }
